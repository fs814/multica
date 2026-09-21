//go:build !windows

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestKnotHTTPLocalProbePinsRequestTarget(t *testing.T) {
	var body []byte
	srv := serveSSEFixture(t, "knot-http-agui-sse.txt", nil, &body)
	defer srv.Close()
	backend := knotHTTPTestBackend(t, srv, map[string]string{KnotClientUUIDEnv: ""})
	fake := filepath.Join(t.TempDir(), "fake-knot")
	const uuid = "68b7d6d7-8eb5-4598-830e-d71bcc739672"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$1\" = client-status ] || exit 2\nprintf '%s\\n' '{\"connection_uuid\":\""+uuid+"\"}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend.cfg.ExecutablePath = fake
	result, _ := drainKnotHTTP(t, backend, ExecOptions{Cwd: "/isolated/task"})
	if result.Status != "completed" {
		t.Fatal(result.Error)
	}
	var sent knotHTTPRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Input.ChatExtra.AgentClientUUID != uuid {
		t.Fatal("local UUID was not sent")
	}
	if len(sent.Input.ChatExtra.Workspace) != 1 || sent.Input.ChatExtra.Workspace[0] != "/isolated/task" {
		t.Fatalf("wrong local workspace: %v", sent.Input.ChatExtra.Workspace)
	}
}
