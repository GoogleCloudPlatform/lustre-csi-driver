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

package upcall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	containerSocketPath = "/csi/ipc.sock"
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

// StartServer starts the Unix domain socket server inside the container.
func StartServer(ctx context.Context) error {
	// Clean up old socket if exists.
	if err := os.Remove(containerSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing socket: %v", err)
	}

	listener, err := net.Listen("unix", containerSocketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on unix socket %s: %w", containerSocketPath, err)
	}

	// Ensure socket permissions allow the root host proxy to connect.
	if err := os.Chmod(containerSocketPath, 0600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("failed to chmod socket: %w", err)
	}

	klog.Infof("Lustre upcall IPC server listening on %s", containerSocketPath)

	go func() {
		<-ctx.Done()
		klog.Info("Shutting down Lustre upcall IPC server")
		_ = listener.Close()
		_ = os.Remove(containerSocketPath)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			klog.Errorf("Failed to accept connection: %v", err)
			continue
		}

		go handleConnection(ctx, conn)
	}
}

func handleConnection(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var req Request
	// Limit request reading payload size to 1MB to protect against resource exhaustion.
	limitedReader := io.LimitReader(conn, 1024*1024)
	decoder := json.NewDecoder(limitedReader)
	if err := decoder.Decode(&req); err != nil {
		sendResponse(conn, Response{ExitCode: 1, Error: fmt.Sprintf("invalid request format: %v", err)})
		return
	}

	resp := executeCommand(ctx, req)
	sendResponse(conn, resp)
}

func sendResponse(conn net.Conn, resp Response) {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(resp); err != nil {
		klog.Errorf("Failed to encode IPC response: %v", err)
	}
}

func executeCommand(ctx context.Context, req Request) Response {
	if len(req.Args) == 0 {
		return Response{ExitCode: 1, Error: "empty command arguments"}
	}

	binaryPath := req.Args[0]
	cmdArgs := req.Args[1:]

	// Strict Security Whitelist Validation.
	if err := validateCommand(binaryPath, cmdArgs); err != nil {
		klog.Errorf("Security rejection: %v", err)
		return Response{ExitCode: 1, Error: fmt.Sprintf("security rejection: %v", err)}
	}

	klog.V(4).Infof("Executing whitelisted upcall: %v", req.Args)

	// Enforce a strict execution timeout boundary (45s) to protect against resource exhaustion from hung binaries.
	// Uses the parent application context so pending upcalls cancel immediately on driver shutdown.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, cmdArgs...)

	// Sanitize incoming environment variables to block execution hijack vectors (e.g. LD_PRELOAD).
	// The os.Environ() comes last, ensuring container variables (like KUBERNETES_SERVICE_HOST) take precedence.
	cleanHostEnv := sanitizeEnv(req.Env)
	cmd.Env = append(cleanHostEnv, os.Environ()...)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()

	if err != nil {
		klog.Errorf("Upcall command %s %v failed: %v, stderr: %s", binaryPath, cmdArgs, err, stderr)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return Response{
				ExitCode: exitErr.ExitCode(),
				Stdout:   stdout,
				Stderr:   stderr,
			}
		}
		return Response{
			ExitCode: 1,
			Error:    err.Error(),
			Stdout:   stdout,
			Stderr:   stderr,
		}
	}

	klog.V(4).Infof("Upcall command %s %v succeeded", binaryPath, cmdArgs)
	return Response{
		ExitCode: 0,
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

func validateCommand(binary string, args []string) error {
	switch binary {
	case "/usr/sbin/lctl":
		if len(args) < 2 || args[0] != "set_param" {
			return fmt.Errorf("unauthorized lctl subcommand or arguments: %v", args)
		}
		return nil
	default:
		return fmt.Errorf("unauthorized binary path execution request: %s", binary)
	}
}

func sanitizeEnv(env []string) []string {
	var clean []string
	for _, v := range env {
		if parts := strings.SplitN(v, "=", 2); len(parts) > 0 {
			key := parts[0]
			// Strip dangerous runtime library hijack variables.
			if strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "PYTHON") || strings.HasPrefix(key, "PERL") {
				klog.Warningf("Security stripping suspicious environment variable from upcall payload: %s", key)
				continue
			}
			clean = append(clean, v)
		}
	}
	return clean
}
