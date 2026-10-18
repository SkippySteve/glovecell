package registration

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// Adapter is one remote-access control client (T3, Orca, or a custom tool).
// The registrar mounts sandbox checkouts on the control host, then asks each
// configured Adapter to register or revoke that local path. Mobile and browser
// sessions attach to the control client itself, not to this process.
type Adapter interface {
	Kind() string
	RegisterPlan(binary, mountPath, title string) (ClientPlan, error)
	RevokePlan(binary, id string) (ClientPlan, error)
	ParseRegister(stdout, stderr string, err error) (id string, already bool, parseErr error)
	ParseRevoke(stdout, stderr string, err error) error
}

var (
	adapterMu sync.RWMutex
	adapters  = map[string]Adapter{}
)

func init() {
	MustRegisterAdapter(t3Adapter{})
	MustRegisterAdapter(orcaAdapter{})
}

// RegisterAdapter adds or replaces a client kind. Builtin kinds may be replaced
// in tests; production custom kinds should use distinct names.
func RegisterAdapter(a Adapter) error {
	if a == nil {
		return fmt.Errorf("adapter required")
	}
	kind := strings.TrimSpace(a.Kind())
	if !validKindName(kind) {
		return fmt.Errorf("invalid client kind %q", kind)
	}
	adapterMu.Lock()
	defer adapterMu.Unlock()
	adapters[kind] = a
	return nil
}

// MustRegisterAdapter panics if registration fails.
func MustRegisterAdapter(a Adapter) {
	if err := RegisterAdapter(a); err != nil {
		panic(err)
	}
}

// LookupAdapter returns a registered client adapter.
func LookupAdapter(kind string) (Adapter, bool) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	a, ok := adapters[strings.TrimSpace(kind)]
	return a, ok
}

// KnownKind reports whether a client kind is registered.
func KnownKind(kind string) bool {
	_, ok := LookupAdapter(kind)
	return ok
}

// ResolvedClient is an adapter plus the binary used to invoke it.
type ResolvedClient struct {
	Adapter Adapter
	Binary  string
}

func validKindName(kind string) bool {
	if kind == "" || len(kind) > 32 {
		return false
	}
	for i, c := range kind {
		switch {
		case c >= 'a' && c <= 'z':
			continue
		case c >= '0' && c <= '9' || c == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ResolveClients builds the active integration list from Options and the
// process environment. Builtin t3/orca use APC_T3_BINARY / APC_ORCA_BINARY.
// Any other kind in CLIENT_INTEGRATIONS loads
// APC_CLIENT_<KIND>_BINARY, APC_CLIENT_<KIND>_REGISTER, and
// APC_CLIENT_<KIND>_REVOKE (comma-separated argv templates with {path},
// {title}, {id}, {binary}).
func ResolveClients(opts Options, getenv func(string) string) ([]ResolvedClient, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	kinds := opts.Integrations
	if len(kinds) == 0 {
		if opts.T3Binary != "" {
			kinds = append(kinds, "t3")
		}
		if opts.OrcaBinary != "" {
			kinds = append(kinds, "orca")
		}
	}
	if len(kinds) == 0 {
		return nil, fmt.Errorf("no client integrations configured")
	}
	out := make([]ResolvedClient, 0, len(kinds))
	seen := map[string]bool{}
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		switch kind {
		case "t3":
			if opts.T3Binary == "" {
				return nil, fmt.Errorf("t3 integration requires APC_T3_BINARY or t3 on PATH")
			}
			a, ok := LookupAdapter("t3")
			if !ok {
				return nil, fmt.Errorf("t3 adapter missing")
			}
			out = append(out, ResolvedClient{Adapter: a, Binary: opts.T3Binary})
		case "orca":
			if opts.OrcaBinary == "" {
				return nil, fmt.Errorf("orca integration requires APC_ORCA_BINARY or orca-ide on PATH")
			}
			a, ok := LookupAdapter("orca")
			if !ok {
				return nil, fmt.Errorf("orca adapter missing")
			}
			out = append(out, ResolvedClient{Adapter: a, Binary: opts.OrcaBinary})
		case "command":
			return nil, fmt.Errorf("use a named custom kind with APC_CLIENT_<KIND>_REGISTER/REVOKE instead of kind %q", kind)
		default:
			a, binary, err := loadCommandAdapter(kind, getenv)
			if err != nil {
				return nil, err
			}
			if err := RegisterAdapter(a); err != nil {
				return nil, err
			}
			out = append(out, ResolvedClient{Adapter: a, Binary: binary})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no client integrations configured")
	}
	return out, nil
}

func loadCommandAdapter(kind string, getenv func(string) string) (Adapter, string, error) {
	if !validKindName(kind) {
		return nil, "", fmt.Errorf("invalid client kind %q", kind)
	}
	prefix := "APC_CLIENT_" + strings.ToUpper(strings.ReplaceAll(kind, "-", "_")) + "_"
	binary := strings.TrimSpace(getenv(prefix + "BINARY"))
	if binary == "" {
		return nil, "", fmt.Errorf("%sBINARY required for client %q", prefix, kind)
	}
	reg := parseArgvTemplate(getenv(prefix + "REGISTER"))
	rev := parseArgvTemplate(getenv(prefix + "REVOKE"))
	if len(reg) == 0 {
		return nil, "", fmt.Errorf("%sREGISTER required for client %q", prefix, kind)
	}
	if len(rev) == 0 {
		return nil, "", fmt.Errorf("%sREVOKE required for client %q", prefix, kind)
	}
	return commandAdapter{kind: kind, register: reg, revoke: rev}, binary, nil
}

func parseArgvTemplate(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
