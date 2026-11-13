package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/forgejo"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/projects"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const RepositoriesLedgerKey = "repositories-policy"

var projectName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

func ValidateProject(p api.RepositoryProject, owner, host string, githubOwner ...string) error {
	if !projectName.MatchString(p.Name) {
		return errors.New("invalid repository project name")
	}
	parts := strings.Split(p.Repository, "/")
	if len(parts) != 2 || forgejo.ValidateRepository(p.Repository, parts[0]) != nil {
		return errors.New("repository must be owner/name")
	}
	switch p.Host {
	case "forgejo":
		if host == "github" || p.Public || forgejo.ValidateRepository(p.Repository, owner) != nil {
			return errors.New("Forgejo enrollment requires the configured owner and repository grant")
		}
	case "github":
		if !p.Public && (len(githubOwner) == 0 || githubOwner[0] == "" || forgejo.ValidateRepository(p.Repository, githubOwner[0]) != nil) {
			return errors.New("private GitHub enrollment requires trusted GitHub authorization; no anonymous fallback")
		}
	default:
		return errors.New("unsupported repository host")
	}
	return nil
}

func (r *Reconciler) validateRepositories(w *api.ProjectWorkspace) error {
	if len(w.Spec.Repositories) > 16 {
		return errors.New("at most sixteen additional repositories")
	}
	names, repositories := map[string]bool{}, map[string]bool{}
	for _, p := range w.Spec.Repositories {
		if err := ValidateProject(p, r.Config.Owner, r.Config.GitHost, r.Config.GitHubOwner); err != nil {
			return err
		}
		key := p.Host + ":" + p.Repository
		if names[p.Name] || repositories[key] || (p.Host == r.Config.GitHost || p.Host == "forgejo" && r.Config.GitHost == "") && p.Repository == w.Spec.Repository {
			return errors.New("duplicate repository enrollment")
		}
		names[p.Name], repositories[key] = true, true
	}
	return nil
}

func projectIdentity(w *api.ProjectWorkspace, p api.RepositoryProject) string {
	sum := sha256.Sum256([]byte(string(w.UID) + "\x00" + p.Name))
	return "apcp-" + hex.EncodeToString(sum[:16])
}

func (r *Reconciler) projectState(ctx context.Context, w *api.ProjectWorkspace, p api.RepositoryProject) (*corev1.Secret, error) {
	key := types.NamespacedName{Namespace: r.Config.Namespace, Name: projectIdentity(w, p)}
	var s corev1.Secret
	raw, _ := json.Marshal(p)
	if err := r.Get(ctx, key, &s); err == nil {
		if err := owner(w, &s); err != nil {
			return nil, err
		}
		if string(s.Data["project"]) != string(raw) || string(s.Data["username"]) != key.Name {
			return nil, errors.New("repository grant ledger binding mismatch")
		}
		return &s, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, err
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return nil, err
	}
	s = corev1.Secret{ObjectMeta: metadata(w, key.Name), Data: map[string][]byte{"project": raw, "username": []byte(key.Name), "password": []byte(hex.EncodeToString(password))}}
	s.Namespace = r.Config.Namespace
	if err := r.Create(ctx, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// enrollRepositories grants and records additional checkouts. When Running and
// projects.json changes, it returns needsWorkerRestart so ensure can refresh
// workers after objects() widens proxy/network policy for the new remotes.
func (r *Reconciler) enrollRepositories(ctx context.Context, w *api.ProjectWorkspace, root *corev1.Secret) (needsWorkerRestart bool, err error) {
	var previous []api.RepositoryProject
	if raw := root.Data[RepositoriesLedgerKey]; len(raw) > 0 && json.Unmarshal(raw, &previous) != nil {
		return false, errors.New("repository enrollment ledger is invalid")
	}
	for _, old := range previous {
		found := false
		for _, p := range w.Spec.Repositories {
			if p == old {
				found = true
			}
		}
		if !found {
			return false, errors.New("individual repository removal requires verified revocation and storage cleanup")
		}
	}
	if len(w.Spec.Repositories) != len(previous) && w.Spec.DesiredState != api.DesiredPaused && w.Spec.DesiredState != api.DesiredRunning {
		return false, errors.New("sandbox must be Running or Paused to enroll repositories")
	}
	var entries []projects.Entry
	for _, p := range w.Spec.Repositories {
		entry := projects.Entry{RepositoryProject: p, Origin: "https://github.com"}
		if p.Host == "github" && !p.Public {
			verified := false
			for _, old := range previous {
				if old == p {
					verified = true
				}
			}
			if !verified {
				if err := r.authorizeGitHubRead(ctx, p); err != nil {
					return false, err
				}
			}
		}
		if p.Host == "forgejo" {
			entry.Origin = r.Config.ForgejoOrigin
			s, err := r.projectState(ctx, w, p)
			if err != nil {
				return false, err
			}
			if string(s.Data["revoked"]) == "true" {
				return false, errors.New("repository revocation is terminal")
			}
			if err := r.current(ctx, w); err != nil {
				return false, err
			}
			token, err := r.Host.Grant(ctx, p.Repository, credentials(s))
			if err != nil {
				return false, err
			}
			if string(s.Data["token"]) != token {
				before := s.DeepCopy()
				s.Data["token"] = []byte(token)
				if err := r.Patch(ctx, s, client.MergeFrom(before)); err != nil {
					return false, err
				}
			}
			entry.Username, entry.Token = string(s.Data["username"]), token
		}
		entries = append(entries, entry)
	}
	// The root recovery ledger never contains an administrative token.
	raw, err := json.Marshal(entries)
	if err != nil {
		return false, err
	}
	if len(entries) > 0 && string(root.Data["projects.json"]) != string(raw) {
		before := root.DeepCopy()
		root.Data["projects.json"] = raw
		if err := r.Patch(ctx, root, client.MergeFrom(before)); err != nil {
			return false, err
		}
		// Defer worker refresh until after objects() so clone sees widened Git access.
		if w.Spec.DesiredState == api.DesiredRunning {
			return true, nil
		}
	}
	return false, nil
}

// restartWorkersForEnrollment deletes worker pods so the Sandbox recreates
// them and bootstrap clones newly enrolled /workspace/projects/<name> paths.
func (r *Reconciler) restartWorkersForEnrollment(ctx context.Context, w *api.ProjectWorkspace) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace(w)), client.MatchingLabels{api.OwnerLabel: string(w.UID), "projects.agentwork.dev/role": "worker"}); err != nil {
		return false, err
	}
	restarted := false
	for i := range pods.Items {
		if err := r.Delete(ctx, &pods.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return restarted, err
		}
		restarted = true
	}
	return restarted, nil
}

func (r *Reconciler) authorizeGitHubRead(ctx context.Context, p api.RepositoryProject) error {
	if r.GitHubToken == "" {
		return errors.New("private GitHub authorization unavailable; configure the trusted repository token")
	}
	request, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+p.Repository, nil)
	if err != nil {
		return errors.New("invalid GitHub repository")
	}
	request.Header.Set("Authorization", "Bearer "+r.GitHubToken)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "agent-project-controller")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("trusted GitHub repository verification unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("GitHub repository authorization unavailable: HTTP %d", response.StatusCode)
	}
	var repo struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&repo) != nil || !strings.EqualFold(repo.FullName, p.Repository) || !strings.EqualFold(repo.CloneURL, "https://github.com/"+p.Repository+".git") {
		return errors.New("GitHub repository identity verification failed")
	}
	return nil
}

func (r *Reconciler) revokeRepositories(ctx context.Context, w *api.ProjectWorkspace) error {
	// Discover persisted intents, including grants created just before a crash.
	var ledgers corev1.SecretList
	if err := r.List(ctx, &ledgers, client.InNamespace(r.Config.Namespace), client.MatchingLabels{api.OwnerLabel: string(w.UID)}); err != nil {
		return err
	}
	found := map[string]bool{}
	for i := range ledgers.Items {
		s := &ledgers.Items[i]
		if !strings.HasPrefix(s.Name, "apcp-") {
			continue
		}
		var p api.RepositoryProject
		if json.Unmarshal(s.Data["project"], &p) != nil || s.Name != projectIdentity(w, p) || string(s.Data["username"]) != s.Name || p.Host != "forgejo" {
			return errors.New("repository revocation ledger is invalid")
		}
		found[p.Name] = true
		if string(s.Data["revoked"]) == "true" {
			continue
		}
		if err := r.Host.Revoke(ctx, p.Repository, credentials(s)); err != nil {
			return err
		}
		before := s.DeepCopy()
		s.Data = map[string][]byte{"project": s.Data["project"], "username": s.Data["username"], "revoked": []byte("true")}
		if err := r.Patch(ctx, s, client.MergeFrom(before)); err != nil {
			return err
		}
	}
	for _, p := range w.Spec.Repositories {
		if p.Host == "forgejo" && !found[p.Name] {
			return errors.New("repository revocation ledger missing; operator recovery required")
		}
	}
	return nil
}
