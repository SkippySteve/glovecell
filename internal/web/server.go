package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/registration"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// dist is the OpenUI console built from web/ (npm run build).
//
//go:embed dist
var dist embed.FS

//go:embed style.css
var style []byte

//go:embed shell.js
var shell []byte

//go:embed console.css
var consoleStyle []byte

type Server struct {
	Client                             client.Client
	Host                               forgejo.Host
	Namespace, Owner, Password, Origin string
	OIDC                               *OIDC
	AllowBasic                         bool
	T3Connect                          bool
	GitHost, GitOrigin                 string
	GitHubOwner                        string
	SandboxControl                     *SandboxControl
	RunRecords                         *RunRecords
	Handoffs                           *Handoffs
}

// openUISpinnerStyle allows the one stylesheet OpenUI's Renderer injects on mount
// (its query-loader keyframes). web/src/factory/csp.test.ts checks the hash still
// matches the installed @openuidev/react-lang.
const openUISpinnerStyle = "'sha256-vai9tBS1dbqDlLt2vYYQwJl16j1hMfOCO5KhCK8LRBg='"

type actorKey struct{}

func equal(a, b string) bool {
	aHash := sha256.Sum256([]byte(a))
	bHash := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(aHash[:], bHash[:]) == 1
}
func (s *Server) Handler() (http.Handler, error) {
	u, e := url.Parse(s.Origin)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || s.Password != "" && len(s.Password) < 32 || s.AllowBasic && len(s.Password) < 32 || s.OIDC == nil && !s.AllowBasic {
		return nil, errors.New("dashboard requires an explicit HTTPS origin and OIDC or explicitly enabled static authentication")
	}
	if s.SandboxControl != nil {
		if e := s.SandboxControl.Validate(); e != nil {
			return nil, e
		}
	}
	if s.RunRecords != nil {
		if e := s.RunRecords.Validate(); e != nil {
			return nil, e
		}
	}
	mux := http.NewServeMux()
	index, e := dist.ReadFile("dist/index.html")
	if e != nil {
		return nil, errors.New("console build is missing; run npm run build in web/")
	}
	assets, _ := fs.Sub(dist, "dist")
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	mux.Handle("GET /assets/", http.FileServerFS(assets))
	mux.HandleFunc("GET /style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(style)
	})
	mux.HandleFunc("GET /shell.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(shell)
	})
	mux.HandleFunc("GET /console.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(consoleStyle)
	})
	mux.HandleFunc("GET /api/v1/repositories", func(w http.ResponseWriter, r *http.Request) {
		repos, e := s.Host.Discover(r.Context())
		if e != nil {
			http.Error(w, "repository discovery unavailable", 502)
			return
		}
		writeJSON(w, repos)
	})
	mux.HandleFunc("GET /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		actor, _ := r.Context().Value(actorKey{}).(string)
		session := map[string]any{"actor": actor, "oidc": s.OIDC != nil, "sandboxControl": s.SandboxControl != nil, "t3Connect": s.T3Connect, "githubOwner": s.GitHubOwner, "runRecords": s.RunRecords != nil}
		for k, v := range s.Handoffs.session() {
			session[k] = v
		}
		writeJSON(w, session)
	})
	if s.RunRecords != nil {
		mux.HandleFunc("GET /api/v1/runs", s.RunRecords.list)
		mux.HandleFunc("GET /api/v1/runs/{id}", s.RunRecords.get)
	}
	if s.Handoffs != nil {
		if e := s.Handoffs.Validate(); e != nil {
			return nil, e
		}
		mux.HandleFunc("POST /api/v1/handoffs", s.Handoffs.create)
		mux.HandleFunc("GET /api/v1/handoffs", s.Handoffs.list)
		mux.HandleFunc("GET /api/v1/handoffs/{name}", s.Handoffs.get)
		mux.HandleFunc("POST /api/v1/orca-projects", s.Handoffs.publishProject)
		mux.HandleFunc("GET /api/v1/orca-projects", s.Handoffs.listProjects)
		mux.HandleFunc("GET /api/v1/orca-projects/{name}", s.Handoffs.getProject)
	}
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		list := &api.ProjectWorkspaceList{}
		if e := s.Client.List(r.Context(), list, client.InNamespace(s.Namespace)); e != nil {
			http.Error(w, "workspace catalog unavailable", 503)
			return
		}
		writeJSON(w, list.Items)
	})
	mux.HandleFunc("POST /api/v1/workspaces", s.create)
	mux.HandleFunc("PATCH /api/v1/workspaces/{name}", s.change)
	mux.HandleFunc("PATCH /api/v1/workspaces/{name}/access", s.changeAccess)
	mux.HandleFunc("POST /api/v1/workspaces/{name}/repositories", s.enrollRepository)
	mux.HandleFunc("GET /api/v1/workspaces/{name}/audit", s.audit)
	mux.HandleFunc("GET /api/v1/workspaces/{name}/clients", s.clients)
	if s.SandboxControl != nil {
		mux.Handle("/sandboxes/", s.SandboxControl.Handler(s.Origin))
		mux.HandleFunc("GET /sandboxes", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/sandboxes/", http.StatusSeeOther) })
	}
	if s.OIDC != nil {
		mux.HandleFunc("POST /auth/logout", s.OIDC.Logout)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' "+openUISpinnerStyle+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if s.OIDC != nil && r.Method == "GET" {
			if r.URL.Path == "/auth/signed-out" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Signed out</title><link rel="stylesheet" href="/style.css"><main class="plain"><h1>You are signed out</h1><p>Your dashboard session has ended. Your identity provider may still have a login session.</p><p><a class="button primary" href="/auth/login">Sign in again</a></p></main></html>`)
				return
			}
			if r.URL.Path == "/style.css" {
				mux.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/auth/login" {
				s.OIDC.Login(w, r)
				return
			}
			if r.URL.Path == "/auth/callback" {
				s.OIDC.Callback(w, r)
				return
			}
		}
		bearer := s.Password != "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && equal(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.Password)
		user, pass, basic := r.BasicAuth()
		basic = s.AllowBasic && s.Password != "" && basic && user == "operator" && equal(pass, s.Password)
		subject := ""
		session := false
		if s.OIDC != nil {
			subject, session = s.OIDC.Subject(r)
		}
		if !bearer && !basic && !session {
			if s.OIDC != nil && r.Method == "GET" && (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/sandboxes")) {
				http.Redirect(w, r, "/auth/login", 302)
				return
			}
			if s.AllowBasic {
				w.Header().Set("WWW-Authenticate", `Basic realm="Project workspaces", charset="UTF-8"`)
			}
			http.Error(w, "authentication required; sign in at /auth/login", 401)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != s.Origin || (basic || session) && origin != s.Origin {
				http.Error(w, "same-origin request required", 403)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && !(s.SandboxControl != nil && strings.HasPrefix(r.URL.Path, "/sandboxes/") && r.Method == "POST") {
				http.Error(w, "JSON required", 415)
				return
			}
		}
		if bearer {
			subject = "automation-token"
		} else if basic {
			subject = "static-operator"
		}
		ctx, cancel := context.WithTimeout(context.WithValue(r.Context(), actorKey{}, subject), 25*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

var errNameInUse = errors.New("project name already exists")

// releaseRemovedName deletes a workspace only after removal has been verified,
// so the same project name can be requested again.
func (s *Server) releaseRemovedName(ctx context.Context, name string) error {
	existing := &api.ProjectWorkspace{}
	if e := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, existing); e != nil {
		if apierrors.IsNotFound(e) {
			return nil
		}
		return e
	}
	if existing.Spec.DesiredState != "Removed" || existing.Status.Phase != "Removed" || !existing.DeletionTimestamp.IsZero() {
		return errNameInUse
	}
	original := append([]string(nil), existing.Finalizers...)
	kept := make([]string, 0, len(original))
	for _, f := range original {
		if f != api.Finalizer {
			kept = append(kept, f)
		}
	}
	existing.Finalizers = kept
	if e := s.Client.Update(ctx, existing); e != nil {
		return e
	}
	if e := s.Client.Delete(ctx, existing); e != nil && !apierrors.IsNotFound(e) {
		return e
	}
	return nil
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name       string          `json:"name"`
		Repository string          `json:"repository"`
		HTTPS      []api.HTTPSRule `json:"https,omitempty"`
		Registries []string        `json:"registries,omitempty"`
	}
	if e := decode(w, r, &request); e != nil || !validName.MatchString(request.Name) || forgejo.ValidateRepository(request.Repository, s.Owner) != nil {
		http.Error(w, "invalid project name or repository", 400)
		return
	}
	rules, e := s.accessRules(request.Repository, request.Registries, request.HTTPS)
	if e != nil {
		http.Error(w, "invalid HTTPS policy", 400)
		return
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	if e := s.releaseRemovedName(r.Context(), request.Name); e != nil {
		if errors.Is(e, errNameInUse) {
			http.Error(w, "project name already exists", 409)
		} else {
			http.Error(w, "project request could not be saved", 503)
		}
		return
	}
	obj := &api.ProjectWorkspace{TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "ProjectWorkspace"}, ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: s.Namespace, Annotations: map[string]string{"projects.agentwork.dev/requested-by": actor}}, Spec: api.WorkspaceSpec{Repository: request.Repository, DesiredState: "Running", HTTPS: rules}}
	if e := s.Client.Create(r.Context(), obj); e != nil {
		if apierrors.IsAlreadyExists(e) {
			http.Error(w, "project name already exists", 409)
		} else {
			http.Error(w, "project request could not be saved", 503)
		}
		return
	}
	w.WriteHeader(201)
	writeJSON(w, obj)
}
func (s *Server) change(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DesiredState    string `json:"desiredState"`
		ResourceVersion string `json:"resourceVersion"`
	}
	if e := decode(w, r, &request); e != nil || request.ResourceVersion == "" || (request.DesiredState != "Running" && request.DesiredState != "Paused" && request.DesiredState != "Removed") {
		http.Error(w, "state and resourceVersion required", 400)
		return
	}
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil {
		http.Error(w, "project unavailable", 404)
		return
	}
	if obj.ResourceVersion != request.ResourceVersion {
		http.Error(w, "project changed; refresh before retrying", 409)
		return
	}
	if !obj.DeletionTimestamp.IsZero() || obj.Spec.DesiredState == "Removed" {
		http.Error(w, "removed projects cannot be reopened; create a new project", 409)
		return
	}
	if request.DesiredState == "Running" && obj.Spec.DesiredState == "Paused" && obj.UID != "" {
		if obj.Status.ObservedGeneration != obj.Generation || obj.Status.Phase != "Paused" || !s.accessApplied(r, obj) {
			http.Error(w, "wait for the paused project and access update to reconcile before resuming", 409)
			return
		}
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations["projects.agentwork.dev/requested-by"] = actor
	obj.Spec.DesiredState = request.DesiredState
	if e := s.Client.Update(r.Context(), obj); e != nil {
		http.Error(w, "project update failed; refresh before retrying", 409)
		return
	}
	writeJSON(w, obj)
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil {
		http.Error(w, "project unavailable", 404)
		return
	}
	var list corev1.ConfigMapList
	if e := s.Client.List(r.Context(), &list, client.InNamespace(s.Namespace), client.MatchingLabels{api.OwnerLabel: string(obj.UID)}); e != nil {
		http.Error(w, "audit unavailable", 503)
		return
	}
	items := []map[string]string{}
	for _, cm := range list.Items {
		if strings.HasPrefix(cm.Name, "audit-") {
			items = append(items, cm.Data)
		}
	}
	writeJSON(w, items)
}

func (s *Server) clients(w http.ResponseWriter, r *http.Request) {
	obj := &api.ProjectWorkspace{}
	if e := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: r.PathValue("name")}, obj); e != nil {
		http.Error(w, "project unavailable", 404)
		return
	}
	cm := &corev1.ConfigMap{}
	name := registration.ConfigMapName(string(obj.UID))
	err := s.Client.Get(r.Context(), types.NamespacedName{Namespace: s.Namespace, Name: name}, cm)
	if apierrors.IsNotFound(err) {
		writeJSON(w, map[string]any{"clients": []any{}, "owners": map[string]string{}})
		return
	}
	if err != nil {
		http.Error(w, "client ledger unavailable", 503)
		return
	}
	if cm.Labels[api.OwnerLabel] != string(obj.UID) {
		http.Error(w, "client ledger ownership mismatch", 409)
		return
	}
	ledger, err := registration.DecodeConfigMapData(cm.Data)
	if err != nil {
		http.Error(w, "client ledger unavailable", 503)
		return
	}
	clients := []map[string]string{}
	for kind, rec := range ledger.Clients {
		clients = append(clients, map[string]string{"kind": kind, "state": rec.State, "id": rec.ID, "path": rec.Path})
	}
	writeJSON(w, map[string]any{"clients": clients, "owners": ledger.Owners})
}
