package providerbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOwnerCommitRecoversTornWritePreservingInode(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python helper unavailable")
	}
	for _, provider := range []string{"codex", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			home := t.TempDir()
			relative := ".codex/auth.json"
			if provider == "cursor" {
				relative = ".config/cursor/auth.json"
			}
			path := filepath.Join(home, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			transaction := oauthRenewal{Stage: "response-saved", Provider: provider, Before: "original-sha", Document: map[string]any{"retained": "private-refresh", "accessToken": "rotated"}}
			input, _ := json.Marshal(transaction)
			marker := filepath.Join(filepath.Dir(path), ".apc-oauth-commit.json")
			if err := os.WriteFile(marker, input, 0600); err != nil {
				t.Fatal(err)
			}
			// Simulate interruption immediately after truncate, before any document bytes.
			if err := os.WriteFile(path, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if os.Getuid() != 1000 {
				if err := os.Chown(path, 1000, 1000); err != nil {
					t.Skip("owner UID fixture unavailable")
				}
			}
			before, _ := os.Stat(path)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			proc := filepath.Join(home, "proc-fixture")
			_ = os.Mkdir(proc, 0700)
			program := strings.Replace(oauthOwnerWrite, "pathlib.Path('/proc')", "pathlib.Path("+strconv.Quote(proc)+")", 1)
			cmd := exec.CommandContext(ctx, "python3", "-c", program)
			cmd.Env = append(os.Environ(), "HOME="+home)
			cmd.Stdin = bytes.NewReader(input)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("recovery failed: %s %v", out, err)
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) {
				t.Fatal("bind-mounted credential inode replaced")
			}
			raw, _ := os.ReadFile(path)
			var document map[string]any
			if json.Unmarshal(raw, &document) != nil || document["accessToken"] != "rotated" {
				t.Fatal("torn credential did not recover")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("committed response copy retained")
			}
			// A changed complete document without a matching write intent is refused.
			_ = os.WriteFile(path, []byte(`{"accessToken":"other-account"}`), 0600)
			cmd = exec.CommandContext(ctx, "python3", "-c", program)
			cmd.Env = append(os.Environ(), "HOME="+home)
			cmd.Stdin = bytes.NewReader(input)
			if cmd.Run() == nil {
				t.Fatal("unrelated owner update overwritten")
			}
		})
	}
}
