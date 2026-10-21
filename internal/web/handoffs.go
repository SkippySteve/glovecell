package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Handoffs forwards tasks to an agent runtime's handoff intake, such as the Orca
// sandbox. The console authenticates the person and adds them as the actor; the
// intake, reachable only from this controller, prepares the repository and
// brief and starts the agent. The console never execs into the runtime. The same
// intake publishes repositories as projects of that runtime, which every client
// paired with it then lists. Every intake request carries Token as a bearer, so
// the intake does not rely on its network policy alone.
//
// Sandbox optionally names the existing sandbox, by its sandbox-control id,
// that runs the intake. The Sandboxes page lists the runtime's projects under
// that sandbox. Without it, the page matches the sandbox whose namespace is the
// intake service's namespace.
type Handoffs struct {
	URL     string
	Token   string
	Sandbox string
	Client  *http.Client
}

const (
	maxHandoffRequest  = 1 << 20
	maxHandoffResponse = 1 << 20
)

var (
	handoffName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	handoffRepo = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	projectName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,99}$`)
	sandboxID   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// minHandoffToken matches the console's own automation secret minimum.
const minHandoffToken = 32

type handoffRequest struct {
	Repo  string            `json:"repo"`
	Name  string            `json:"name"`
	Agent string            `json:"agent"`
	Base  string            `json:"base,omitempty"`
	Brief string            `json:"brief"`
	Data  map[string]string `json:"data,omitempty"`
	Actor string            `json:"actor"`
}

func (h Handoffs) Validate() error {
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("handoff intake %q must be an http(s) origin", h.URL)
	}
	if len(h.Token) < minHandoffToken {
		return fmt.Errorf("handoff intake token must contain at least %d bytes", minHandoffToken)
	}
	if h.Sandbox != "" && !sandboxID.MatchString(h.Sandbox) {
		return fmt.Errorf("handoff sandbox %q must be a sandbox id", h.Sandbox)
	}
	return nil
}

// namespace is the Kubernetes namespace of an in-cluster intake service
// (SERVICE.NAMESPACE.svc...), or "" for any other host.
func (h Handoffs) namespace() string {
	u, err := url.Parse(h.URL)
	if err != nil {
		return ""
	}
	parts := strings.Split(u.Hostname(), ".")
	if len(parts) < 3 || parts[2] != "svc" || !dnsLabel.MatchString(parts[1]) {
		return ""
	}
	return parts[1]
}

// session describes the intake to the console: whether it is there, and which
// existing sandbox holds the runtime's projects.
func (h *Handoffs) session() map[string]any {
	if h == nil {
		return map[string]any{"handoffs": false, "orcaProjects": false}
	}
	out := map[string]any{"handoffs": true, "orcaProjects": true}
	if h.Sandbox != "" {
		out["orcaSandbox"] = h.Sandbox
	} else if ns := h.namespace(); ns != "" {
		out["orcaNamespace"] = ns
	}
	return out
}

func (h Handoffs) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 20 * time.Second, Transport: transport}
}

// relay sends one request to the intake and copies its bounded JSON answer.
func (h Handoffs) relay(w http.ResponseWriter, r *http.Request, method, path string, body []byte) (int, error) {
	u, _ := url.Parse(h.URL)
	u.Path = path
	req, err := http.NewRequestWithContext(r.Context(), method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+h.Token)
	resp, err := h.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHandoffResponse+1))
	if err != nil {
		return 0, err
	}
	if len(data) > maxHandoffResponse {
		return 0, errors.New("handoff intake response is too large")
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, fmt.Errorf("handoff intake: %s", resp.Status)
	}
	// The intake refusing the console's token is a configuration fault, not the caller's.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, fmt.Errorf("handoff intake refused the console token: %s", resp.Status)
	}
	if json.Valid(data) {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(data)
	return resp.StatusCode, nil
}

func (h Handoffs) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxHandoffRequest)
	var req handoffRequest
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		http.Error(w, "invalid handoff", http.StatusBadRequest)
		return
	}
	if req.Actor != "" {
		http.Error(w, "actor is set by the console", http.StatusBadRequest)
		return
	}
	if !handoffRepo.MatchString(req.Repo) || !handoffName.MatchString(req.Name) || (req.Agent != "claude" && req.Agent != "codex") || req.Brief == "" {
		http.Error(w, "handoff needs repo OWNER/NAME, a lowercase name, agent claude or codex, and a brief", http.StatusBadRequest)
		return
	}
	req.Actor, _ = r.Context().Value(actorKey{}).(string)
	body, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "invalid handoff", http.StatusBadRequest)
		return
	}
	status, err := h.relay(w, r, http.MethodPost, "/handoffs", body)
	slog.InfoContext(r.Context(), "handoff requested", "actor", req.Actor, "repo", req.Repo, "name", req.Name, "agent", req.Agent, "data_files", len(req.Data), "status", status, "error", errString(err))
	if err != nil {
		http.Error(w, "The sandbox did not accept the handoff; check its records before retrying.", http.StatusBadGateway)
	}
}

func (h Handoffs) list(w http.ResponseWriter, r *http.Request) {
	if _, err := h.relay(w, r, http.MethodGet, "/handoffs", nil); err != nil {
		http.Error(w, "handoff records unavailable", http.StatusBadGateway)
	}
}

func (h Handoffs) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !handoffName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	if _, err := h.relay(w, r, http.MethodGet, "/handoffs/"+name, nil); err != nil {
		http.Error(w, "handoff record unavailable", http.StatusBadGateway)
	}
}

type projectRequest struct {
	Repo  string `json:"repo"`
	Actor string `json:"actor"`
}

// publishProject asks the intake to clone a repository into the runtime and
// register it as a project there, without starting an agent.
func (h Handoffs) publishProject(w http.ResponseWriter, r *http.Request) {
	var req projectRequest
	if err := decode(w, r, &req); err != nil {
		http.Error(w, "invalid project", http.StatusBadRequest)
		return
	}
	if req.Actor != "" {
		http.Error(w, "actor is set by the console", http.StatusBadRequest)
		return
	}
	if !handoffRepo.MatchString(req.Repo) || len(req.Repo) > 200 {
		http.Error(w, "project needs repo OWNER/NAME", http.StatusBadRequest)
		return
	}
	req.Actor, _ = r.Context().Value(actorKey{}).(string)
	body, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "invalid project", http.StatusBadRequest)
		return
	}
	status, err := h.relay(w, r, http.MethodPost, "/projects", body)
	slog.InfoContext(r.Context(), "orca project publish requested", "actor", req.Actor, "repo", req.Repo, "status", status, "error", errString(err))
	if err != nil {
		http.Error(w, "The sandbox did not accept the project; check its records before retrying.", http.StatusBadGateway)
	}
}

func (h Handoffs) listProjects(w http.ResponseWriter, r *http.Request) {
	if _, err := h.relay(w, r, http.MethodGet, "/projects", nil); err != nil {
		http.Error(w, "project records unavailable", http.StatusBadGateway)
	}
}

func (h Handoffs) getProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !projectName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	if _, err := h.relay(w, r, http.MethodGet, "/projects/"+name, nil); err != nil {
		http.Error(w, "project record unavailable", http.StatusBadGateway)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
