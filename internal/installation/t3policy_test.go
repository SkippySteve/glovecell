package installation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// T3 Code 0.1.11 added /api/preview/tunnel: any client with terminal access can
// reach every loopback port of the T3 host through it, outside the cell
// gateway's approvals and audit (docs/design/port-tunnels.md, section 7). An
// image that bundles T3 0.1.11 or later must therefore ship T3's managed policy
// at /etc/t3code/policy.json with "limits": {"previewTunnel": false}.
//
// Older T3 versions refuse to start with a policy key they do not know, so the
// file is required only from 0.1.11 on.

var t3Release = regexp.MustCompile(`pingdotgg/t3code/releases/download/v(\d+)\.(\d+)\.(\d+)/`)
var t3PolicyCopy = regexp.MustCompile(`(?m)^COPY\s+(?:--\S+\s+)*(\S+)\s+/etc/t3code/policy\.json\s*$`)

func t3TunnelVersion(major, minor, patch int) bool {
	if major != 0 {
		return major > 0
	}
	if minor != 1 {
		return minor > 1
	}
	return patch >= 11
}

// checkT3PreviewTunnelPolicy returns the problems with one Dockerfile. read
// loads a file relative to the build context.
func checkT3PreviewTunnelPolicy(dockerfile string, read func(string) ([]byte, error)) []string {
	pins := t3Release.FindAllStringSubmatch(dockerfile, -1)
	if len(pins) == 0 {
		return []string{"no T3 release pin found; update this check if the download URL changed"}
	}
	needsPolicy := false
	for _, pin := range pins {
		major, _ := strconv.Atoi(pin[1])
		minor, _ := strconv.Atoi(pin[2])
		patch, _ := strconv.Atoi(pin[3])
		if t3TunnelVersion(major, minor, patch) {
			needsPolicy = true
		}
	}
	if !needsPolicy {
		return nil
	}
	copies := t3PolicyCopy.FindAllStringSubmatch(dockerfile, -1)
	if len(copies) == 0 {
		return []string{"T3 0.1.11 or later is bundled without COPY <file> /etc/t3code/policy.json turning off its preview tunnel"}
	}
	var problems []string
	for _, c := range copies {
		raw, err := read(c[1])
		if err != nil {
			problems = append(problems, fmt.Sprintf("T3 policy %s: %v", c[1], err))
			continue
		}
		var policy struct {
			SchemaVersion int    `json:"schemaVersion"`
			Organization  string `json:"organization"`
			Limits        struct {
				PreviewTunnel *bool `json:"previewTunnel"`
			} `json:"limits"`
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			problems = append(problems, fmt.Sprintf("T3 policy %s: %v", c[1], err))
			continue
		}
		if policy.SchemaVersion != 1 || strings.TrimSpace(policy.Organization) == "" {
			problems = append(problems, fmt.Sprintf("T3 policy %s: needs schemaVersion 1 and an organization, or T3 refuses to start", c[1]))
		}
		if policy.Limits.PreviewTunnel == nil || *policy.Limits.PreviewTunnel {
			problems = append(problems, fmt.Sprintf("T3 policy %s: limits.previewTunnel must be false", c[1]))
		}
	}
	return problems
}

func TestBundledT3HasPreviewTunnelOff(t *testing.T) {
	raw, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string) ([]byte, error) { return os.ReadFile(filepath.Join("../..", path)) }
	for _, problem := range checkT3PreviewTunnelPolicy(string(raw), read) {
		t.Error(problem)
	}
}

func TestT3PreviewTunnelPolicyCheck(t *testing.T) {
	const old = "RUN curl -fsSL https://github.com/pingdotgg/t3code/releases/download/v0.1.9/t3-0.1.9-linux-x64.tar.gz -o /tmp/t3.tar.gz\n"
	const tunnel = "RUN curl -fsSL https://github.com/pingdotgg/t3code/releases/download/v0.1.11/t3-0.1.11-linux-x64.tar.gz -o /tmp/t3.tar.gz\n"
	const later = "RUN curl -fsSL https://github.com/pingdotgg/t3code/releases/download/v0.2.0/t3-0.2.0-linux-x64.tar.gz -o /tmp/t3.tar.gz\n"
	const copyPolicy = "COPY --chmod=0444 images/t3/policy.json /etc/t3code/policy.json\n"
	files := map[string]string{
		"off":     `{"schemaVersion": 1, "organization": "Glovecell", "limits": {"previewTunnel": false}}`,
		"on":      `{"schemaVersion": 1, "organization": "Glovecell", "limits": {"previewTunnel": true}}`,
		"missing": `{"schemaVersion": 1, "organization": "Glovecell", "limits": {}}`,
		"noorg":   `{"schemaVersion": 1, "limits": {"previewTunnel": false}}`,
		"broken":  `{"schemaVersion": 1,`,
	}
	reader := func(name string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if path != "images/t3/policy.json" {
				return nil, os.ErrNotExist
			}
			return []byte(files[name]), nil
		}
	}
	cases := []struct {
		name, dockerfile, policy string
		ok                       bool
	}{
		{"0.1.9 needs no policy", old, "", true},
		{"no pin is refused", "FROM scratch\n", "", false},
		{"0.1.11 without policy is refused", tunnel, "", false},
		{"0.2.0 without policy is refused", later, "", false},
		{"0.1.11 with tunnel off passes", tunnel + copyPolicy, "off", true},
		{"0.1.11 with tunnel on is refused", tunnel + copyPolicy, "on", false},
		{"0.1.11 with the limit missing is refused", tunnel + copyPolicy, "missing", false},
		{"0.1.11 without organization is refused", tunnel + copyPolicy, "noorg", false},
		{"0.1.11 with unreadable JSON is refused", tunnel + copyPolicy, "broken", false},
		{"one old pin does not hide a new one", old + tunnel, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := checkT3PreviewTunnelPolicy(c.dockerfile, reader(c.policy))
			if c.ok && len(problems) > 0 {
				t.Fatalf("want pass, got %v", problems)
			}
			if !c.ok && len(problems) == 0 {
				t.Fatal("want refusal, got pass")
			}
		})
	}
}
