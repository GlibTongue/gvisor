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

package harness

import (
	"context"
	"fmt"
	"net"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
)

// WaitUntilServing uses the given container to check if server is
// serving on port 'port'. WaitUntilServing takes ownership of 'netcat'.
func WaitUntilServing(ctx context.Context, netcat *dockerutil.Container, server net.IP, port int) error {
	defer netcat.CleanUp(ctx)

	cmd := fmt.Sprintf("while ! nc -zv %s %d; do true; done", server.String(), port)
	_, err := netcat.Run(ctx, dockerutil.RunOpts{
		Image: "packetdrill",
	}, "sh", "-c", cmd)
	return err
}
