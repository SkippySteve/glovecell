package store

import (
	"fmt"
	"strconv"
	"time"
)

// History actions written by the store itself, in the same transaction as
// the change they describe.
const (
	ActionRequestCreated  = "access-requested"
	ActionRequestApproved = "request-approved"
	ActionRequestDenied   = "request-denied"
	ActionApprovalRevoked = "approval-revoked"
)

func validateNewRequest(n NewRequest) error {
	if n.WorkspaceUID == "" || n.RequestedBy == "" || len(n.Reason) > 1024 {
		return fmt.Errorf("%w: request needs a workspace and requester; reason at most 1024 bytes", ErrInvalid)
	}
	if err := n.Target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func validateDecision(d Decision) error {
	if d.WorkspaceUID == "" || d.RequestID <= 0 || d.Actor == "" || len(d.Reason) > 1024 {
		return fmt.Errorf("%w: decision needs a workspace, request and approver; reason at most 1024 bytes", ErrInvalid)
	}
	return nil
}

func validateSummary(x ProxySummary) error {
	if x.WorkspaceUID == "" || x.RequestID == "" || len(x.RequestID) > 128 || x.Time.IsZero() || (x.Decision != ProxyAllow && x.Decision != ProxyDeny) {
		return fmt.Errorf("%w: summary needs workspace, request ID, time and allow or deny", ErrInvalid)
	}
	if err := x.Target.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// URL renders a Target for messages.
func (t Target) URL() string {
	port := ""
	if !(t.Scheme == "https" && t.Port == 443) && !(t.Scheme == "http" && t.Port == 80) {
		port = ":" + strconv.Itoa(t.Port)
	}
	return t.Scheme + "://" + t.Host + port + t.Path
}

func targetDetails(t Target) map[string]string {
	d := map[string]string{"scheme": t.Scheme, "host": t.Host, "port": strconv.Itoa(t.Port), "method": t.Method, "path": t.Path}
	if t.Fingerprint != "" {
		d["fingerprint"] = t.Fingerprint
	}
	if p := t.Package; p != nil {
		d["package"] = p.Ecosystem + "/" + p.Name + "@" + p.Version
	}
	return d
}

func requestEvent(r AccessRequest) Event {
	d := targetDetails(r.Target)
	d["request"] = strconv.FormatInt(r.ID, 10)
	if r.Reason != "" {
		d["reason"] = r.Reason
	}
	return Event{
		Key: fmt.Sprintf("request-%d-created", r.ID), WorkspaceUID: r.WorkspaceUID, Workspace: r.Workspace,
		Action: ActionRequestCreated, Actor: r.RequestedBy, Source: "console", Time: r.CreatedAt, Details: d,
		Message: fmt.Sprintf("Access requested by %s: %s %s (request %d)", r.RequestedBy, r.Target.Method, r.Target.URL(), r.ID),
	}
}

func decisionEvent(r AccessRequest, a *Approval, d Decision) Event {
	details := targetDetails(r.Target)
	details["request"] = strconv.FormatInt(r.ID, 10)
	if d.Group != "" {
		details["approverGroup"] = d.Group
	}
	if d.Reason != "" {
		details["reason"] = d.Reason
	}
	action, verb := ActionRequestDenied, "denied"
	if d.Approve {
		action, verb = ActionRequestApproved, "approved"
		details["approval"] = strconv.FormatInt(a.ID, 10)
	}
	at := time.Now().UTC()
	if r.DecidedAt != nil {
		at = *r.DecidedAt
	}
	return Event{
		Key: fmt.Sprintf("request-%d-%s", r.ID, verb), WorkspaceUID: r.WorkspaceUID, Workspace: r.Workspace,
		Action: action, Actor: d.Actor, Source: "console", Time: at, Details: details,
		Message: fmt.Sprintf("%s %s %s (request %d) by %s", verbTitle(verb), r.Target.Method, r.Target.URL(), r.ID, d.Actor),
	}
}

func revocationEvent(a Approval, r Revocation) Event {
	details := targetDetails(a.Target)
	details["approval"] = strconv.FormatInt(a.ID, 10)
	if r.Group != "" {
		details["approverGroup"] = r.Group
	}
	if r.Reason != "" {
		details["reason"] = r.Reason
	}
	at := time.Now().UTC()
	if a.RevokedAt != nil {
		at = *a.RevokedAt
	}
	return Event{
		Key: fmt.Sprintf("approval-%d-revoked", a.ID), WorkspaceUID: a.WorkspaceUID, Workspace: a.Workspace,
		Action: ActionApprovalRevoked, Actor: r.Actor, Source: "console", Time: at, Details: details,
		Message: fmt.Sprintf("Revoked approval %d for %s %s by %s", a.ID, a.Target.Method, a.Target.URL(), r.Actor),
	}
}

func verbTitle(v string) string {
	if v == "" {
		return v
	}
	return string(v[0]-'a'+'A') + v[1:]
}
