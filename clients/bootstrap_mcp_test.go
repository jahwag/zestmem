package clients_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytelope/zestmem/internal/startup"
)

func TestBootstrapMCP(t *testing.T) {
	bridge := filepath.Join(t.TempDir(), "memory-mcp-bridge")
	fake := `#!/bin/sh
[ "$1" = "--startup-context" ] || exit 2
[ "${MEMORY_MCP_LOCAL_UNAUTHENTICATED:-}" = "1" ] || exit 3
[ "${MEMORY_STARTUP_SPACE_ID:-}" = "${EXPECTED_SPACE:-}" ] || exit 4
[ "${FAKE_FAIL:-}" != "1" ] || exit 5
printf '%s' "${FAKE_CONTEXT:-}"
`
	if err := os.WriteFile(bridge, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	valid := startup.Prefix + `{"spaces":[],"omitted_count":0}` + "\n"
	tests := []struct {
		name       string
		context    string
		fail       bool
		debug      bool
		wantOutput string
		wantError  string
	}{
		{name: "valid", context: valid, wantOutput: valid},
		{name: "wrong prefix", context: "untrusted\n"},
		{name: "over limit", context: startup.Prefix + strings.Repeat("x", startup.MaxContextBytes-len(startup.Prefix)), debug: true, wantError: "limit is 800"},
		{name: "bridge failure", fail: true, debug: true, wantError: "sidecar request failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", "./bootstrap-mcp.sh")
			command.Env = append(os.Environ(),
				"MEMORY_MCP_BRIDGE="+bridge,
				"MEMORY_STARTUP_SPACE_ID=49400172-ec60-4646-92c8-331d1db78a4a",
				"EXPECTED_SPACE=49400172-ec60-4646-92c8-331d1db78a4a",
				"FAKE_CONTEXT="+test.context,
			)
			if test.fail {
				command.Env = append(command.Env, "FAKE_FAIL=1")
			}
			if test.debug {
				command.Env = append(command.Env, "ZESTMEM_BOOTSTRAP_DEBUG=1")
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("bootstrap: %v: %s", err, output)
			}
			if string(output) != test.wantOutput && (test.wantError == "" || !strings.Contains(string(output), test.wantError)) {
				t.Fatalf("output=%q, want output=%q or error containing %q", output, test.wantOutput, test.wantError)
			}
		})
	}
}
