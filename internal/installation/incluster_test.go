package installation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docs/in-cluster.md: Glovecell runs in the cluster, installed by the chart.

type component struct {
	Command     string `json:"command"`
	Class       string `json:"class"`
	Template    string `json:"template"`
	Where       string `json:"where"`
	Replacement string `json:"replacement"`
	Tracking    string `json:"tracking"`
}

// May only shrink: remove an entry when its component moves into the chart.
var allowedExceptions = map[string]bool{"provider-broker": true, "github-broker": true, "hubspoke": true, "register": true}

func loadComponents(t *testing.T) map[string]component {
	t.Helper()
	raw, err := os.ReadFile("../../docs/components.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Components []component }
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]component{}
	for _, c := range doc.Components {
		if _, dup := out[c.Command]; dup {
			t.Fatalf("duplicate component %q", c.Command)
		}
		out[c.Command] = c
	}
	return out
}

// Subcommands of the top-level `switch os.Args[1]` in cmd/agent-project-controller.
func subcommands(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../cmd/agent-project-controller/main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "\t\tswitch os.Args[1] {\n")
	if start < 0 {
		t.Fatal("top-level subcommand switch not found")
	}
	body := src[start:]
	end := strings.Index(body, "\n\t\t}\n")
	if end < 0 {
		t.Fatal("end of subcommand switch not found")
	}
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\t\tcase (.+):$`).FindAllStringSubmatch(body[:end], -1) {
		for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			if !strings.HasPrefix(q[1], "-") {
				names = append(names, q[1])
			}
		}
	}
	if len(names) < 10 {
		t.Fatalf("parsed only %d subcommands: %v", len(names), names)
	}
	return names
}

func TestEverySubcommandIsClassified(t *testing.T) {
	components := loadComponents(t)
	seen := map[string]bool{}
	for _, name := range subcommands(t) {
		seen[name] = true
		if _, ok := components[name]; !ok {
			t.Errorf("subcommand %q is not in docs/components.json: say where it runs (docs/in-cluster.md)", name)
		}
	}
	for name := range components {
		if !seen[name] {
			t.Errorf("docs/components.json lists %q, which is no longer a subcommand", name)
		}
	}
}

func TestComponentsRunInTheCluster(t *testing.T) {
	var exceptions []string
	for name, c := range loadComponents(t) {
		switch c.Class {
		case "chart":
			if c.Template == "" {
				t.Errorf("%s: chart component needs its template", name)
			} else if _, err := os.Stat(filepath.Join("../../charts/agent-project-controller/templates", c.Template)); err != nil {
				t.Errorf("%s: template %s: %v", name, c.Template, err)
			}
		case "controller-pod", "operator-cli":
		case "cluster-exception", "desktop-exception":
			exceptions = append(exceptions, name)
			if c.Where == "" || c.Replacement == "" || c.Tracking == "" {
				t.Errorf("%s: exception needs where, replacement and tracking", name)
			}
			if !allowedExceptions[name] {
				t.Errorf("%s: new exception; Glovecell components run in the cluster, installed by the chart (docs/in-cluster.md)", name)
			}
		default:
			t.Errorf("%s: unknown class %q", name, c.Class)
		}
	}
	sort.Strings(exceptions)
	t.Logf("remaining exceptions: %v", exceptions)
}

// Files allowed to mention desktop services: they document existing
// exceptions or this rule. Remove a file once its exception is retired.
var desktopServiceDocs = map[string]bool{
	"AGENTS.md":          true, // states the rule
	"docs/in-cluster.md": true, // states the rule and the exceptions
	"docs/clients.md":    true, // desktop registrar, until retired
}

func TestNoDocsInstallDesktopServices(t *testing.T) {
	pattern := regexp.MustCompile(`systemctl --user|\.config/systemd/user|kubectl[^\n]*port-forward`)
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch rel {
			case ".git", "node_modules", "third_party", "web/node_modules", "internal/web/dist":
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".md" && ext != ".sh" && ext != ".yaml" && ext != ".yml" && ext != ".tpl" && ext != ".txt" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if pattern.Match(raw) && !desktopServiceDocs[filepath.ToSlash(rel)] {
			t.Errorf("%s tells an operator to run a desktop service; Glovecell runs in the cluster (docs/in-cluster.md)", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
