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
	"net"
	"os"
	"strings"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name    string
		binary  string
		args    []string
		wantErr bool
		errSub  string
	}{
		{
			name:    "Valid lctl set_param osc wildcard param",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.max_dirty_mb=32"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param osc dynamic import target",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.lustre-OST0000-osc-ffff8801.import=connection=10.0.0.1@tcp::1"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param mdc wildcard param",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "mdc.*.max_rpcs_in_flight=16"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param mdc dynamic import target",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "mdc.lustre-MDT0000-mdc-ffff8801.import=connection=10.0.0.1@tcp::1"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param llite wildcard param",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "llite.*.max_read_ahead_mb=256"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param ldlm namespace param",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "ldlm.namespaces.*.lru_size=2000"},
			wantErr: false,
		},
		{
			name:    "Valid lctl set_param multiple allowed params",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.max_dirty_mb=32", "llite.*.max_read_ahead_mb=256"},
			wantErr: false,
		},
		{
			name:    "Valid l_gssiam_upcall",
			binary:  "/usr/sbin/l_gssiam_upcall",
			args:    []string{"-o", "user"},
			wantErr: false,
		},
		{
			name:    "Invalid lctl subcommand",
			binary:  "/usr/sbin/lctl",
			args:    []string{"get_param", "osc.*.max_dirty_mb"},
			wantErr: true,
			errSub:  "unauthorized lctl subcommand",
		},
		{
			name:    "Empty lctl arguments",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param"},
			wantErr: true,
			errSub:  "unauthorized lctl subcommand or arguments",
		},
		{
			name:    "Unauthorized binary",
			binary:  "/bin/rm",
			args:    []string{"-rf", "/"},
			wantErr: true,
			errSub:  "unauthorized binary path execution request",
		},
		{
			name:    "Unauthorized lctl option flag -n",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "-n", "osc.*.max_dirty_mb=32"},
			wantErr: true,
			errSub:  "unauthorized lctl option/flag",
		},
		{
			name:    "Unauthorized lctl option flag -P",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "-P", "osc.*.max_dirty_mb=32"},
			wantErr: true,
			errSub:  "unauthorized lctl option/flag",
		},
		{
			name:    "Unauthorized lctl parameter key mdt",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "mdt.*.identity_upcall=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key ptlrpc",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "ptlrpc.lsvcgss.upcall=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key unlisted generic",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "debug=0"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key jobid_var",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "jobid_var=procname_uid"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with upcall keyword in osc",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.upcall=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with wildcard bypass in osc",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.up*all=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with question mark wildcard bypass in osc",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.up?all=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with character class wildcard bypass in osc",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.up[c]all=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with wildcard in parameter name",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.*=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with backslash in parameter name",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*.up\\call=/tmp/evil"},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key prefix only",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc."},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
		{
			name:    "Unauthorized lctl parameter key with empty leaf component",
			binary:  "/usr/sbin/lctl",
			args:    []string{"set_param", "osc.*."},
			wantErr: true,
			errSub:  "unauthorized lctl parameter key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCommand(tt.binary, tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errSub) {
				t.Errorf("expected error to contain %q, got %q", tt.errSub, err.Error())
			}
		})
	}
}

func TestServerEndToEndMock(t *testing.T) {
	// Temporarily redirect socket path for testing
	tmpSock := "./test_ipc.sock"
	_ = os.Remove(tmpSock)

	// Override default socket for testing by manually setting up listener logic in a goroutine,
	// or we can temporarily modify ContainerSocketPath if it wasn't a constant.
	// Since ContainerSocketPath is a constant, let's create a custom listener for the test handler.
	listener, err := net.Listen("unix", tmpSock)
	if err != nil {
		t.Fatalf("failed to listen on test socket: %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(tmpSock)
	}()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleConnection(context.Background(), conn)
		}
	}()

	// Test 1: Unauthorized command
	clientConn, err := net.Dial("unix", tmpSock)
	if err != nil {
		t.Fatalf("failed to dial test socket: %v", err)
	}

	req := Request{
		Args: []string{"/bin/sh", "-c", "evil"},
		Env:  os.Environ(),
	}
	_ = json.NewEncoder(clientConn).Encode(req)

	var resp Response
	_ = json.NewDecoder(clientConn).Decode(&resp)
	_ = clientConn.Close()

	if resp.ExitCode == 0 || !strings.Contains(resp.Error, "security rejection") {
		t.Errorf("expected security rejection for unauthorized binary, got resp: %+v", resp)
	}

	// Test 2: Whitelisted binary that doesn't exist in host test env
	clientConn2, err := net.Dial("unix", tmpSock)
	if err != nil {
		t.Fatalf("failed to dial test socket second time: %v", err)
	}

	req2 := Request{
		Args: []string{"/usr/sbin/lctl", "set_param", "osc.*.max_dirty_mb=32"},
		Env:  os.Environ(),
	}
	_ = json.NewEncoder(clientConn2).Encode(req2)

	var resp2 Response
	_ = json.NewDecoder(clientConn2).Decode(&resp2)
	_ = clientConn2.Close()

	// It should pass validation and fail on execution because /usr/sbin/lctl doesn't exist on the test runner host
	if !strings.Contains(resp2.Error, "no such file or directory") {
		t.Errorf("expected execution to fail due to missing binary, but expected validation to pass. Got resp: %+v", resp2)
	}

	// Test 3: IAM upcall routing with full path
	clientConn3, err := net.Dial("unix", tmpSock)
	if err != nil {
		t.Fatalf("failed to dial test socket third time: %v", err)
	}

	req3 := Request{
		Args: []string{"/etc/udev/l_gssiam_upcall", "arg1", "arg2"},
		Env:  os.Environ(),
	}
	_ = json.NewEncoder(clientConn3).Encode(req3)

	var resp3 Response
	_ = json.NewDecoder(clientConn3).Decode(&resp3)
	_ = clientConn3.Close()

	// It should pass validation (routed to /usr/sbin/l_gssiam_upcall) and fail on execution
	if !strings.Contains(resp3.Error, "/usr/sbin/l_gssiam_upcall") || !strings.Contains(resp3.Error, "no such file or directory") {
		t.Errorf("expected routing to /usr/sbin/l_gssiam_upcall to pass validation but fail execution for full path. Got resp: %+v", resp3)
	}
}
