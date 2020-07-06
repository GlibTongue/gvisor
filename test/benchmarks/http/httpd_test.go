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
package http

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"testing"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/benchmarks/harness"
)

var h harness.Harness

// BenchmarkHttpdThreads iterates over different thread counts.
// How well the runtime under test handles parallel connections.
func BenchmarkHttpdThreads(b *testing.B) {
	// Grab a machine for the client and server.
	clientMachine, err := h.GetMachine()
	if err != nil {
		b.Fatalf("failed to get machine: %v", err)
	}

	serverMachine, err := h.GetMachine()
	if err != nil {
		b.Fatalf("failed to get machine: %v", err)
	}

	// The test iterates over client threads, so set other parameters.
	requests := 1000
	threads := []int{1, 5, 10, 25}
	doc := "latin10k.txt" // see Dockerfile '//images/benchmarks/ab'

	for _, t := range threads {
		b.Run(fmt.Sprintf("%dThreads", t), func(b *testing.B) {
			runHttpd(b, clientMachine, serverMachine, doc, requests, t)
		})
	}
}

// BenchmarkHttpdDocSize iterates over different sized payloads, testing how
// well the runtime handles different payload sizes.
func BenchmarkHttpdDocSize(b *testing.B) {
	clientMachine, err := h.GetMachine()
	if err != nil {
		b.Fatalf("failed to get machine: %v", err)
	}

	serverMachine, err := h.GetMachine()
	if err != nil {
		b.Fatalf("failed to get machine: %v", err)
	}

	requests := 1000
	threads := 1
	docs := []string{"notfound"}
	for _, val := range []int{1, 10, 100, 1000, 1024, 10240} {
		// see Dockerfile '//images/benchmarks/ab'
		docs = append(docs, fmt.Sprintf("latin%dK.txt", val))
	}

	for _, doc := range docs {
		b.Run(doc, func(b *testing.B) {
			runHttpd(b, clientMachine, serverMachine, doc, requests, threads)
		})
	}
}

// runHttpd runs a single test run.
func runHttpd(b *testing.B, clientMachine, serverMachine harness.Machine, doc string, requests, numThreads int) {
	b.Helper()

	// Grab a container from the server.
	ctx := context.Background()
	server := serverMachine.GetContainer(ctx, b)
	defer server.CleanUp(ctx)

	// Copy the docs to /tmp and serve from there.
	cmd := "mkdir -p /tmp/html; cp -r /local /tmp/html/.; apache2 -X"
	port := 80

	// Start the server.
	server.Spawn(ctx, dockerutil.RunOpts{
		Image: "benchmarks/httpd",
		Ports: []int{port},
	}, "sh", "-c", cmd)

	ip, err := server.FindIP(ctx)
	if err != nil {
		b.Fatalf("failed to find server ip: %v", err)
	}

	// Check the server is serving.
	harness.WaitUntilServing(ctx, clientMachine.GetContainer(ctx, b), ip, port)

	// Grab a client.
	client := clientMachine.GetContainer(ctx, b)
	defer client.CleanUp(ctx)

	path := fmt.Sprintf("http://%s:%d/%s", ip.String(), port, doc)
	// See apachebench (ab) for flags.
	cmd = fmt.Sprintf("ab -n %d -c %d %s", requests, numThreads, path)

	out, err := client.Run(ctx, dockerutil.RunOpts{
		Image: "benchmarks/ab",
	}, "sh", "-c", cmd)
	if err != nil {
		b.Fatalf("run failed with: %v", err)
	}

	// Parse and report custom metrics.
	transferRate, err := parseTransferRate(out)
	if err != nil {
		b.Logf("failed to parse transferrate: %v")
	}
	b.ReportMetric(transferRate, "transferRate[kB/s]")

	latency, err := parseLatency(out)
	if err != nil {
		b.Logf("failed to parse latency: %v")
	}
	b.ReportMetric(latency, "meanLatency[ms]")

	reqPerSecond, err := parseRequestsPerSecond(out)
	if err != nil {
		b.Logf("failed to parse requests per second: %v")
	}
	b.ReportMetric(reqPerSecond, "requests_per_second")
}

// Parses Transfer Rate from apachebench output.
func parseTransferRate(data string) (float64, error) {
	re := regexp.MustCompile(`Transfer rate:\s+(\d+\.?\d+?)\s+\[Kbytes/sec\]\s+received`)
	match := re.FindStringSubmatch(data)
	if len(match) < 1 {
		return 0, fmt.Errorf("failed get bandwidth: %s", data)
	}
	return strconv.ParseFloat(match[1], 64)
}

// Parses Latency  from apachebench output.
func parseLatency(data string) (float64, error) {
	re := regexp.MustCompile(`Total:\s+\d+\s+(\d+)\s+(\d+\.?\d+?)\s+\d+\s+\d+\s`)
	match := re.FindStringSubmatch(data)
	if len(match) < 1 {
		return 0, fmt.Errorf("failed get bandwidth: %s", data)
	}
	return strconv.ParseFloat(match[1], 64)

}

// Parses RequestsPerSecond from apachebench output.
func parseRequestsPerSecond(data string) (float64, error) {
	re := regexp.MustCompile(`Requests per second:\s+(\d+\.?\d+?)\s+`)
	match := re.FindStringSubmatch(data)
	if len(match) < 1 {
		return 0, fmt.Errorf("failed get bandwidth: %s", data)
	}
	return strconv.ParseFloat(match[1], 64)

}

// Sample output from apachebench.
var sampleData = `This is ApacheBench, Version 2.3 <$Revision: 1826891 $>
Copyright 1996 Adam Twiss, Zeus Technology Ltd, http://www.zeustech.net/
Licensed to The Apache Software Foundation, http://www.apache.org/

Benchmarking 10.10.10.10 (be patient).....done


Server Software:        Apache/2.4.38
Server Hostname:        10.10.10.10
Server Port:            80

Document Path:          /latin10k.txt
Document Length:        210 bytes

Concurrency Level:      1
Time taken for tests:   0.180 seconds
Complete requests:      100
Failed requests:        0
Non-2xx responses:      100
Total transferred:      38800 bytes
HTML transferred:       21000 bytes
Requests per second:    556.44 [#/sec] (mean)
Time per request:       1.797 [ms] (mean)
Time per request:       1.797 [ms] (mean, across all concurrent requests)
Transfer rate:          210.84 [Kbytes/sec] received

Connection Times (ms)
              min  mean[+/-sd] median   max
Connect:        0    0   0.2      0       2
Processing:     1    2   1.0      1       8
Waiting:        1    1   1.0      1       7
Total:          1    2   1.2      1      10

Percentage of the requests served within a certain time (ms)
  50%      1
  66%      2
  75%      2
  80%      2
  90%      2
  95%      3
  98%      7
  99%     10
 100%     10 (longest request)`

// TestParsers checks the parsers work.
func TestParsers(t *testing.T) {
	want := 210.84
	got, err := parseTransferRate(sampleData)
	if err != nil {
		t.Fatalf("failed to parse transfer rate with error: %v")
	} else if got != want {
		t.Fatalf("got: %f, want: %f", got, want)
	}

	want = 2.0
	got, err = parseLatency(sampleData)
	if err != nil {
		t.Fatalf("failed to parse transfer rate with error: %v")
	} else if got != want {
		t.Fatalf("got: %f, want: %f", got, want)
	}

	want = 556.44
	got, err = parseRequestsPerSecond(sampleData)
	if err != nil {
		t.Fatalf("failed to parse transfer rate with error: %v")
	} else if got != want {
		t.Fatalf("got: %f, want: %f", got, want)
	}

}

func TestMain(m *testing.M) {
	h.Init()
	os.Exit(m.Run())
}
