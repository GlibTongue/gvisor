// Copyright 2020 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dockerutil

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"gvisor.dev/gvisor/pkg/test/testutil"
)

// Container represents a Docker Container allowing
// user to configure and control as one would with the 'docker'
// client. Container is backed by the offical golang docker API.
// See: https://pkg.go.dev/github.com/docker/docker.
type Container struct {
	Name    string
	Runtime string

	logger   testutil.Logger
	client   *client.Client
	id       string
	mounts   []mount.Mount
	links    []string
	cleanups []func()
	copyErr  error

	// Stores streams attached to the container. Used by WaitForOutputSubmatch.
	streams types.HijackedResponse

	// stores previously read data from the attached streams.
	streamBuf bytes.Buffer
}

// RunOpts are options for running a container.
type RunOpts struct {
	// Image is the image relative to images/. This will be mangled
	// appropriately, to ensure that only first-party images are used.
	Image string

	// Memory is the memory limit in bytes.
	Memory int

	// Cpus in which to allow execution. ("0", "1", "0-2").
	CpusetCpus string

	// Ports are the ports to be allocated.
	Ports []int

	// WorkDir sets the working directory.
	WorkDir string

	// ReadOnly sets the read-only flag.
	ReadOnly bool

	// Env are additional environment variables.
	Env []string

	// User is the user to use.
	User string

	// Privileged enables privileged mode.
	Privileged bool

	// CapAdd are the extra set of capabilities to add.
	CapAdd []string

	// CapDrop are the extra set of capabilities to drop.
	CapDrop []string

	// Mounts is the list of directories/files to be mounted inside the container.
	Mounts []mount.Mount

	// Links is the list of containers to be connected to the container.
	Links []string
}

// MakeContainer sets up the struct for a Docker container.
//
// Names of containers will be unique.
func MakeContainer(ctx context.Context, logger testutil.Logger) *Container {
	// Slashes are not allowed in container names.
	name := testutil.RandomID(logger.Name())
	name = strings.ReplaceAll(name, "/", "-")
	client, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return nil
	}

	client.NegotiateAPIVersion(ctx)

	return &Container{
		logger:  logger,
		Name:    name,
		Runtime: *runtime,
		client:  client,
	}
}

// Spawn is analogous to 'docker run -d'.
func (c *Container) Spawn(ctx context.Context, r RunOpts, args ...string) error {
	if err := c.create(ctx, r, args); err != nil {
		return err
	}
	return c.Start(ctx)
}

// SpawnProcess is analogous to 'docker run -it'. It returns a process
// which represents the root process.
func (c *Container) SpawnProcess(ctx context.Context, r RunOpts, args ...string) (Process, error) {
	config, hostconf, netconf := c.ConfigsFrom(r, args...)
	config.Tty = true
	config.OpenStdin = true

	if err := c.CreateFrom(ctx, config, hostconf, netconf); err != nil {
		return Process{}, err
	}

	if err := c.Start(ctx); err != nil {
		return Process{}, err
	}

	return Process{container: c, conn: c.streams}, nil
}

// Run is analogous to 'docker run'.
func (c *Container) Run(ctx context.Context, r RunOpts, args ...string) (string, error) {
	if err := c.create(ctx, r, args); err != nil {
		return "", err
	}

	if err := c.Start(ctx); err != nil {
		return "", err
	}

	if err := c.Wait(ctx); err != nil {
		return "", err
	}

	return c.Logs(ctx)
}

// ConfigsFrom returns container configs from RunOpts and args. The caller should call 'CreateFrom'
// and Start.
func (c *Container) ConfigsFrom(r RunOpts, args ...string) (*container.Config, *container.HostConfig, *network.NetworkingConfig) {
	return c.config(r, args), c.hostConfig(r), &network.NetworkingConfig{}
}

// MakeLink formats a link to add to a RunOpts.
func (c *Container) MakeLink(target string) string {
	return fmt.Sprintf("%s:%s", c.Name, target)
}

// CreateFrom creates a container from the given configs.
func (c *Container) CreateFrom(ctx context.Context, conf *container.Config, hostconf *container.HostConfig, netconf *network.NetworkingConfig) error {
	cont, err := c.client.ContainerCreate(ctx, conf, hostconf, netconf, c.Name)
	if err != nil {
		return err
	}
	c.id = cont.ID
	return nil
}

// Create is analogous to 'docker create'.
func (c *Container) Create(ctx context.Context, r RunOpts, args ...string) error {
	return c.create(ctx, r, args)
}

func (c *Container) create(ctx context.Context, r RunOpts, args []string) error {
	conf := c.config(r, args)
	hostconf := c.hostConfig(r)
	cont, err := c.client.ContainerCreate(ctx, conf, hostconf, nil, c.Name)
	if err != nil {
		return err
	}
	c.id = cont.ID
	return nil
}

func (c *Container) config(r RunOpts, args []string) *container.Config {
	ports := nat.PortSet{}
	for _, p := range r.Ports {
		port := nat.Port(fmt.Sprintf("%d", p))
		ports[port] = struct{}{}
	}
	env := append(r.Env, fmt.Sprintf("RUNSC_TEST_NAME=%s", c.Name))

	return &container.Config{
		Image:        testutil.ImageByName(r.Image),
		Cmd:          args,
		ExposedPorts: ports,
		Env:          env,
		WorkingDir:   r.WorkDir,
		User:         r.User,
	}
}

func (c *Container) hostConfig(r RunOpts) *container.HostConfig {
	c.mounts = append(c.mounts, r.Mounts...)

	return &container.HostConfig{
		Runtime:         c.Runtime,
		Mounts:          c.mounts,
		PublishAllPorts: true,
		Links:           r.Links,
		CapAdd:          r.CapAdd,
		CapDrop:         r.CapDrop,
		Privileged:      r.Privileged,
		ReadonlyRootfs:  r.ReadOnly,
		Resources: container.Resources{
			Memory:     int64(r.Memory), // In bytes.
			CpusetCpus: r.CpusetCpus,
		},
	}
}

// Start is analogous to 'docker start'.
func (c *Container) Start(ctx context.Context) error {

	// Open a connection to the container for parsing logs and for TTY.
	streams, err := c.client.ContainerAttach(ctx, c.id,
		types.ContainerAttachOptions{
			Stream: true,
			Stdin:  true,
			Stdout: true,
			Stderr: true,
		})
	if err != nil {
		return fmt.Errorf("failed to connect to container: %v", err)
	}

	c.streams = streams
	c.cleanups = append(c.cleanups, func() {
		c.streams.Close()
	})

	return c.client.ContainerStart(ctx, c.id, types.ContainerStartOptions{})
}

// Stop is analogous to 'docker stop'.
func (c *Container) Stop(ctx context.Context) error {
	return c.client.ContainerStop(ctx, c.id, nil)
}

// Pause is analogous to'docker pause'.
func (c *Container) Pause(ctx context.Context) error {
	return c.client.ContainerPause(ctx, c.id)
}

// Unpause is analogous to 'docker unpause'.
func (c *Container) Unpause(ctx context.Context) error {
	return c.client.ContainerUnpause(ctx, c.id)
}

// Checkpoint is analogous to 'docker checkpoint'.
func (c *Container) Checkpoint(ctx context.Context, name string) error {
	return c.client.CheckpointCreate(ctx, c.Name, types.CheckpointCreateOptions{CheckpointID: name, Exit: true})
}

// Restore is analogous to 'docker start --checkname [name]'.
func (c *Container) Restore(ctx context.Context, name string) error {
	return c.client.ContainerStart(ctx, c.id, types.ContainerStartOptions{CheckpointID: name})
}

// Logs is analogous 'docker logs'.
func (c *Container) Logs(ctx context.Context) (string, error) {
	var out bytes.Buffer
	err := c.logs(ctx, &out, &out)
	return out.String(), err
}

func (c *Container) logs(ctx context.Context, stdout, stderr *bytes.Buffer) error {
	opts := types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true}
	writer, err := c.client.ContainerLogs(ctx, c.id, opts)
	if err != nil {
		return err
	}
	defer writer.Close()
	_, err = stdcopy.StdCopy(stdout, stderr, writer)

	return err
}

// ID returns the container id.
func (c *Container) ID() string {
	return c.id
}

// SandboxPid returns the container's pid.
func (c *Container) SandboxPid(ctx context.Context) (int, error) {
	resp, err := c.client.ContainerInspect(ctx, c.id)
	if err != nil {
		return -1, err
	}
	return resp.ContainerJSONBase.State.Pid, nil
}

// FindIP returns the IP address of the container.
func (c *Container) FindIP(ctx context.Context) (net.IP, error) {
	resp, err := c.client.ContainerInspect(ctx, c.id)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(resp.NetworkSettings.DefaultNetworkSettings.IPAddress)
	if ip == nil {
		return net.IP{}, fmt.Errorf("invalid IP: %q", ip)
	}
	return ip, nil
}

// FindPort returns the host port that is mapped to 'sandboxPort'.
func (c *Container) FindPort(ctx context.Context, sandboxPort int) (int, error) {
	desc, err := c.client.ContainerInspect(ctx, c.id)
	if err != nil {
		return -1, fmt.Errorf("error retrieving port: %v", err)
	}

	format := fmt.Sprintf("%d/tcp", sandboxPort)
	ports, ok := desc.NetworkSettings.Ports[nat.Port(format)]
	if !ok {
		return -1, fmt.Errorf("error retrieving port: %v", err)

	}

	port, err := strconv.Atoi(ports[0].HostPort)
	if err != nil {
		return -1, fmt.Errorf("error parsing port %q: %v", port, err)
	}
	return port, nil
}

// CopyFiles copies in and mounts the given files. They are always ReadOnly.
func (c *Container) CopyFiles(opts *RunOpts, target string, sources ...string) {
	dir, err := ioutil.TempDir("", c.Name)
	if err != nil {
		c.copyErr = fmt.Errorf("ioutil.TempDir failed: %v", err)
		return
	}
	c.cleanups = append(c.cleanups, func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0755); err != nil {
		c.copyErr = fmt.Errorf("os.Chmod(%q, 0755) failed: %v", dir, err)
		return
	}
	for _, name := range sources {
		src, err := testutil.FindFile(name)
		if err != nil {
			c.copyErr = fmt.Errorf("testutil.FindFile(%q) failed: %v", name, err)
			return
		}
		dst := path.Join(dir, path.Base(name))
		if err := testutil.Copy(src, dst); err != nil {
			c.copyErr = fmt.Errorf("testutil.Copy(%q, %q) failed: %v", src, dst, err)
			return
		}
		c.logger.Logf("copy: %s -> %s", src, dst)
	}
	opts.Mounts = append(opts.Mounts, mount.Mount{
		Type:     mount.TypeBind,
		Source:   dir,
		Target:   target,
		ReadOnly: false,
	})
}

// Status inspects the container returns its status.
func (c *Container) Status(ctx context.Context) (types.ContainerState, error) {
	resp, err := c.client.ContainerInspect(ctx, c.id)
	if err != nil {
		return types.ContainerState{}, err
	}
	return *resp.State, err
}

// Wait waits for the container to exit.
func (c *Container) Wait(ctx context.Context) error {
	statusChan, errChan := c.client.ContainerWait(ctx, c.id, container.WaitConditionNotRunning)
	select {
	case err := <-errChan:
		return err
	case <-statusChan:
		return nil
	}
}

// WaitTimeout waits for the container to exit with a timeout.
func (c *Container) WaitTimeout(ctx context.Context, timeout time.Duration) error {
	timeoutChan := time.After(timeout)
	statusChan, errChan := c.client.ContainerWait(ctx, c.id, container.WaitConditionNotRunning)
	select {
	case err := <-errChan:
		return err
	case <-statusChan:
		return nil
	case <-timeoutChan:
		return fmt.Errorf("container %s timed out after %v seconds", c.Name, timeout.Seconds())
	}
}

// WaitForOutput searches container logs for pattern and returns or timesout.
func (c *Container) WaitForOutput(ctx context.Context, pattern string, timeout time.Duration) (string, error) {
	matches, err := c.WaitForOutputSubmatch(ctx, pattern, timeout)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("didn't find pattern %s logs", pattern)
	}
	return matches[0], nil
}

// WaitForOutputSubmatch searches container logs for the given
// pattern or times out. It returns any regexp submatches as well.
func (c *Container) WaitForOutputSubmatch(ctx context.Context, pattern string, timeout time.Duration) ([]string, error) {
	re := regexp.MustCompile(pattern)
	if matches := re.FindStringSubmatch(c.streamBuf.String()); matches != nil {
		return matches, nil
	}

	for exp := time.Now().Add(timeout); time.Now().Before(exp); {
		c.streams.Conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
		_, err := stdcopy.StdCopy(&c.streamBuf, &c.streamBuf, c.streams.Reader)

		if err != nil {
			// check that it wasn't a timeout
			if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
				return nil, err
			}
		}

		if matches := re.FindStringSubmatch(c.streamBuf.String()); matches != nil {
			return matches, nil
		}
	}

	return nil, fmt.Errorf("timeout waiting for output %q: out: %s", re.String(), c.streamBuf.String())
}

// Kill kills the container.
func (c *Container) Kill(ctx context.Context) error {
	return c.client.ContainerKill(ctx, c.id, "")
}

// Remove is analogous to 'docker rm'.
func (c *Container) Remove(ctx context.Context) error {
	// Remove the image.
	remove := types.ContainerRemoveOptions{
		RemoveVolumes: c.mounts != nil,
		RemoveLinks:   c.links != nil,
		Force:         true,
	}
	return c.client.ContainerRemove(ctx, c.Name, remove)
}

// CleanUp kills and deletes the container (best effort).
func (c *Container) CleanUp(ctx context.Context) {
	// Kill the container.
	if err := c.Kill(ctx); err != nil && !strings.Contains(err.Error(), "is not running") {
		// Just log; can't do anything here.
		c.logger.Logf("error killing container %q: %v", c.Name, err)
	}
	// Remove the image.
	if err := c.Remove(ctx); err != nil {
		c.logger.Logf("error removing container %q: %v", c.Name, err)
	}
	// Forget all mounts.
	c.mounts = nil
	// Execute all cleanups.
	for _, c := range c.cleanups {
		c()
	}
	c.cleanups = nil
}
