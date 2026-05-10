/*
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

const (
	defaultSocketPath = "/var/lib/kubelet/plugins/lustre.csi.storage.gke.io/ipc.sock"
	socketEnvVar      = "LUSTRE_IPC_SOCKET"
	timeoutDuration   = 60 * time.Second
)

type Request struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

type Response struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [args...]\n", os.Args[0])
		os.Exit(1)
	}

	socketPath := os.Getenv(socketEnvVar)
	if socketPath == "" {
		socketPath = defaultSocketPath
	}

	req := Request{
		Args: os.Args[1:],
		Env:  os.Environ(),
	}

	payload, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling request: %v\n", err)
		os.Exit(1)
	}

	var conn net.Conn
	const maxRetries = 5
	const retryInterval = 1 * time.Second

	for i := range maxRetries {
		conn, err = net.DialTimeout("unix", socketPath, timeoutDuration)
		if err == nil {
			break
		}
		fmt.Fprintf(os.Stderr, "Attempt %d: failed to connect to CSI IPC socket %s: %v. Retrying in %v...\n", i+1, socketPath, err, retryInterval)
		time.Sleep(retryInterval)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Fatal: could not connect to CSI IPC socket %s after %d attempts: %v\n", socketPath, maxRetries, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	// Set deadlines for read/write operations.
	_ = conn.SetDeadline(time.Now().Add(timeoutDuration))

	// Write the payload.
	_, err = conn.Write(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing payload to socket: %v\n", err)
		os.Exit(1)
	}

	// Close writing side to signal EOF to the container's json.NewDecoder.
	// Do not remove CloseWrite() otherwise the container server will block indefinitely.
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.CloseWrite()
	}

	// Read response.
	var resp Response
	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding response from socket: %v\n", err)
		os.Exit(1)
	}

	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Container execution error: %s\n", resp.Error)
	}

	if resp.Stdout != "" {
		_, _ = os.Stdout.WriteString(resp.Stdout)
	}
	if resp.Stderr != "" {
		_, _ = os.Stderr.WriteString(resp.Stderr)
	}

	os.Exit(resp.ExitCode)
}
