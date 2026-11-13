package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	defaultAuditAssignmentFeedConfigMap = "glovecell-audit-assignments"
	auditAssignmentFeedKey              = "assignments.json"
	auditAssignmentFeedLabel            = "projects.agentwork.dev/audit-assignment-feed"
)

var (
	apcAssignmentKey = regexp.MustCompile(`^apc-[0-9a-f]{32}/project$`)
	safeAssignment   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{0,127}$`)
)

// auditAssignment is the host runsc-wrapper assignment payload for one cell.
type auditAssignment struct {
	Slot     string `json:"slot"`
	Project  string `json:"project"`
	Task     string `json:"task"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (r *Reconciler) auditAssignmentFeedConfigMapName() string {
	if r != nil && r.Config.AuditAssignmentFeedConfigMap != "" {
		return r.Config.AuditAssignmentFeedConfigMap
	}
	return defaultAuditAssignmentFeedConfigMap
}

func (r *Reconciler) auditAssignmentKey(w *api.ProjectWorkspace) string {
	return namespace(w) + "/project"
}

func (r *Reconciler) auditAssignmentFor(w *api.ProjectWorkspace) (auditAssignment, error) {
	project := w.Name
	if project == "" || !safeAssignment.MatchString(project) {
		return auditAssignment{}, fmt.Errorf("workspace name is not a safe audit assignment project value")
	}
	a := auditAssignment{
		Slot:     "glovecell",
		Project:  project,
		Task:     "workspace",
		Provider: "multi",
		Model:    "multi",
	}
	key := r.auditAssignmentKey(w)
	if !apcAssignmentKey.MatchString(key) {
		return auditAssignment{}, fmt.Errorf("workspace namespace is outside the apc assignment class")
	}
	return a, nil
}

func decodeAuditAssignmentFeed(raw string) (map[string]auditAssignment, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]auditAssignment{}, nil
	}
	var doc map[string]auditAssignment
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]auditAssignment{}
	}
	for key, value := range doc {
		if !apcAssignmentKey.MatchString(key) {
			return nil, fmt.Errorf("feed key outside apc project class: %s", key)
		}
		if err := validateAuditAssignment(value); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return doc, nil
}

func validateAuditAssignment(a auditAssignment) error {
	for _, part := range []string{a.Slot, a.Project, a.Task, a.Provider, a.Model} {
		if !safeAssignment.MatchString(part) {
			return fmt.Errorf("unsafe assignment value")
		}
	}
	return nil
}

func encodeAuditAssignmentFeed(doc map[string]auditAssignment) (string, error) {
	if doc == nil {
		doc = map[string]auditAssignment{}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// syncAuditAssignmentFeed upserts or removes this workspace's host assignment
// entry in the aggregated ConfigMap. Off by default (AuditAssignmentFeed).
func (r *Reconciler) syncAuditAssignmentFeed(ctx context.Context, w *api.ProjectWorkspace, present bool) error {
	if r == nil || !r.Config.AuditAssignmentFeed {
		return nil
	}
	key := r.auditAssignmentKey(w)
	var desired *auditAssignment
	if present {
		a, err := r.auditAssignmentFor(w)
		if err != nil {
			return err
		}
		desired = &a
	}
	name := r.auditAssignmentFeedConfigMapName()
	for attempt := 0; attempt < 5; attempt++ {
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Namespace: r.Config.Namespace, Name: name}, cm)
		if apierrors.IsNotFound(err) {
			if desired == nil {
				return nil
			}
			body, err := encodeAuditAssignmentFeed(map[string]auditAssignment{key: *desired})
			if err != nil {
				return err
			}
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: r.Config.Namespace,
					Labels:    map[string]string{auditAssignmentFeedLabel: "true"},
				},
				Data: map[string]string{auditAssignmentFeedKey: body},
			}
			if err := r.Create(ctx, cm); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return err
			}
			return nil
		}
		if err != nil {
			return err
		}
		doc, err := decodeAuditAssignmentFeed(cm.Data[auditAssignmentFeedKey])
		if err != nil {
			return err
		}
		changed := false
		if desired == nil {
			if _, ok := doc[key]; ok {
				delete(doc, key)
				changed = true
			}
		} else if cur, ok := doc[key]; !ok || cur != *desired {
			doc[key] = *desired
			changed = true
		}
		if !changed {
			return nil
		}
		body, err := encodeAuditAssignmentFeed(doc)
		if err != nil {
			return err
		}
		updated := cm.DeepCopy()
		updated.Data = map[string]string{auditAssignmentFeedKey: body}
		if updated.Labels == nil {
			updated.Labels = map[string]string{}
		}
		updated.Labels[auditAssignmentFeedLabel] = "true"
		if err := r.Update(ctx, updated); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("audit assignment feed update conflicted repeatedly")
}
