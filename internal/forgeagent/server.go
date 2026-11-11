// Package forgeagent keeps the Forgejo administrative token out of the
// controller. The agent runs in its own pod, holds the token, and serves only
// the three operations the controller needs (Discover, Grant, Revoke), each
// narrower than the token: workspace identities only (apc-/apcp-<32 hex>),
// repositories of the configured owner only. A compromised controller can
// still grant its own workspace identities, but can no longer use Forgejo
// administration for anything else (create admins, read other owners'
// repositories, change site settings).
//
// The controller authenticates with a projected ServiceAccount token whose
// audience is the agent's; the agent checks it with a TokenReview and accepts
// exactly one ServiceAccount.
package forgeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Audience is the token audience the controller requests and the agent checks.
const Audience = "agent-project-controller-forge-agent"

const maxBody = 16 << 10

// identityPattern matches the workspace identities the controller creates:
// apc-<workspace UID without dashes> and apcp-<repository grant hash>.
var identityPattern = regexp.MustCompile(`^apcp?-[0-9a-f]{32}$`)

var errUnauthenticated = errors.New("unauthenticated")

// Reviewer returns the username a bearer token authenticates as.
type Reviewer interface {
	Review(ctx context.Context, token string) (string, error)
}

// TokenReviewer checks tokens with the Kubernetes TokenReview API.
type TokenReviewer struct {
	Client   kubernetes.Interface
	Audience string
}

func (t TokenReviewer) Review(ctx context.Context, token string) (string, error) {
	res, err := t.Client.AuthenticationV1().TokenReviews().Create(ctx, &authv1.TokenReview{Spec: authv1.TokenReviewSpec{Token: token, Audiences: []string{t.Audience}}}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	if !res.Status.Authenticated || !slices.Contains(res.Status.Audiences, t.Audience) {
		return "", errUnauthenticated
	}
	return res.Status.User.Username, nil
}

// Server is the agent's HTTP API.
type Server struct {
	Host     forgejo.Host
	Owner    string
	Reviewer Reviewer
	// Caller is the only accepted username, for example
	// system:serviceaccount:agent-project-controller:controller.
	Caller string
}

type grantRequest struct {
	Repository string `json:"repository"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Token      string `json:"token,omitempty"`
}

func (g grantRequest) validate(owner string) error {
	if err := forgejo.ValidateRepository(g.Repository, owner); err != nil {
		return err
	}
	if !identityPattern.MatchString(g.Username) {
		return errors.New("only workspace identities are managed here")
	}
	if !printable(g.Password, 1, 256) || g.Token != "" && !printable(g.Token, 1, 256) {
		return errors.New("invalid workspace credentials")
	}
	return nil
}

func printable(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return
	}
	ctx := r.Context()
	switch r.URL.Path {
	case "/v1/discover":
		if len(bytes.TrimSpace(raw)) != 0 && string(bytes.TrimSpace(raw)) != "{}" {
			writeError(w, http.StatusBadRequest, "discover takes no arguments")
			return
		}
		repos, err := s.Host.Discover(ctx)
		if err != nil {
			s.forgeError(w, "discover", err)
			return
		}
		writeJSON(w, map[string]any{"repositories": repos})
	case "/v1/grant", "/v1/revoke":
		var req grantRequest
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request")
			return
		}
		if err := req.validate(s.Owner); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		cred := forgejo.Credentials{Username: req.Username, Password: req.Password, Token: req.Token}
		if r.URL.Path == "/v1/grant" {
			token, err := s.Host.Grant(ctx, req.Repository, cred)
			if err != nil {
				s.forgeError(w, "grant", err)
				return
			}
			slog.InfoContext(ctx, "forge grant", "repository", req.Repository, "identity", req.Username)
			writeJSON(w, map[string]string{"token": token})
			return
		}
		if err := s.Host.Revoke(ctx, req.Repository, cred); err != nil {
			s.forgeError(w, "revoke", err)
			return
		}
		slog.InfoContext(ctx, "forge revoke", "repository", req.Repository, "identity", req.Username)
		writeJSON(w, map[string]string{})
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" || s.Reviewer == nil || s.Caller == "" {
		return false
	}
	user, err := s.Reviewer.Review(r.Context(), token)
	if err != nil {
		if !errors.Is(err, errUnauthenticated) {
			slog.WarnContext(r.Context(), "forge agent token review failed")
		}
		return false
	}
	return user == s.Caller
}

// forgeError passes the Forgejo client's message through: it never contains
// credentials (status codes and protection rule names only), and the
// controller shows it as the workspace condition, as it did in-process.
func (s *Server) forgeError(w http.ResponseWriter, op string, err error) {
	slog.Warn("forge operation failed", "operation", op, "status", err.Error())
	writeError(w, http.StatusBadGateway, err.Error())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
