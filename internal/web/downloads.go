package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Downloads is "Download into sandbox" for project sandboxes.
//
//  1. Check: the operator enters a URL. For each hop the controller lets the
//     project proxy reach that host (a short-lived Cilium policy on the proxy
//     pod only), gives the proxy a one-time probe grant for the exact URL, and
//     runs `agent-project-controller fetch` (mode probe) in the worker. The
//     worker sends one HEAD through its own proxy; the proxy answers without a
//     body and records the status and Location. The controller reads that
//     record from the proxy, never from the worker, and follows the Location
//     only if it is https, public, not a loop and within MaxHops.
//  2. The console shows the chain. Download approves exactly that chain
//     (by digest). The controller grants each hop again, one-time and exact,
//     and runs the helper in fetch mode, which writes the file as the worker's
//     user. A redirect that leaves the approved chain stops the download and
//     is shown for re-approval.
//
// The controller never fetches the bytes itself. It execs only in the
// project's own apc-<uid> namespace (admission policy project-controller-exec-scope).
type Downloads struct {
	MaxBytes int64
	Timeout  time.Duration
	// Exec runs a command in a container of a project pod, streaming stdin and stdout.
	Exec func(ctx context.Context, namespace, pod, container string, command []string, stdin io.Reader, stdout, stderr io.Writer) error
	// Policy builds the per-operation proxy egress policy.
	Policy network.DownloadPolicy
	// Settle waits after a policy change so the dataplane applies it before
	// the proxy resolves the host. Zero means two seconds.
	Settle time.Duration

	mu     sync.Mutex
	checks map[string]*checkRecord
	jobs   map[string]*jobRecord
	busy   map[string]string // workspace UID -> operation id
	cancel map[string]context.CancelFunc
	now    func() time.Time
}

// Fixed worker-side settings: the project proxy service, the read-only
// inspection CA mount and the worker's home (reconciler.go objects()).
const (
	workerProxy   = "http://proxy:3128"
	workerCAFile  = "/trust/ca.crt"
	workerHome    = "/workspace/home"
	maxBusy       = 4
	checkLifetime = 15 * time.Minute
	keepJobs      = 20
)

var schemaCNPList = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicyList"}
var helperCommand = []string{"/usr/local/bin/agent-project-controller", "fetch"}
var grantCommand = []string{"/usr/local/bin/agent-project-controller", "download-grant"}
var projectNamespacePattern = regexp.MustCompile(`^apc-[0-9a-f]{32}$`)
var operationID = regexp.MustCompile(`^[a-z0-9]{12}$`)

// Check is the console's view of a redirect-chain check.
type Check struct {
	ID                 string         `json:"id"`
	Workspace          string         `json:"workspace"`
	Input              string         `json:"input"`
	State              string         `json:"state"` // checking, ready, failed
	Hops               []download.Hop `json:"hops"`
	Digest             string         `json:"digest,omitempty"`
	Error              string         `json:"error,omitempty"`
	DefaultDestination string         `json:"defaultDestination"`
	RequestedBy        string         `json:"requestedBy"`
	Started            time.Time      `json:"started"`
	Expires            time.Time      `json:"expires"`
}

// Job is the console's view of one download.
type Job struct {
	ID                 string           `json:"id"`
	Workspace          string           `json:"workspace"`
	CheckID            string           `json:"checkId"`
	Input              string           `json:"input"`
	State              string           `json:"state"` // starting, downloading, done, failed, changed, cancelled
	Hops               []download.Hop   `json:"hops"`
	Hop                int              `json:"hop"`
	Followed           []string         `json:"followed"`
	Destination        string           `json:"destination"`
	Overwrite          bool             `json:"overwrite"`
	ExpectedSHA256     string           `json:"expectedSha256,omitempty"`
	AcceptQueryChanges bool             `json:"acceptQueryChanges"`
	MaxBytes           int64            `json:"maxBytes"`
	Bytes              int64            `json:"bytes"`
	Total              int64            `json:"total"`
	SHA256             string           `json:"sha256,omitempty"`
	Path               string           `json:"path,omitempty"`
	Code               string           `json:"code,omitempty"`
	Error              string           `json:"error,omitempty"`
	Change             *download.Change `json:"change,omitempty"`
	Note               string           `json:"note,omitempty"`
	RequestedBy        string           `json:"requestedBy"`
	Started            time.Time        `json:"started"`
	Finished           *time.Time       `json:"finished,omitempty"`
}

type checkRecord struct {
	Check
	uid string
}
type jobRecord struct {
	Job
	uid string
}

func (d *Downloads) init() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checks == nil {
		d.checks, d.jobs, d.busy, d.cancel = map[string]*checkRecord{}, map[string]*jobRecord{}, map[string]string{}, map[string]context.CancelFunc{}
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.MaxBytes <= 0 {
		d.MaxBytes = download.DefaultMaxBytes
	}
	if d.Timeout <= 0 {
		d.Timeout = download.DefaultTimeout
	}
}

// Validate checks operator configuration.
func (d *Downloads) Validate() error {
	if d.Exec == nil || d.Policy == nil {
		return errors.New("downloads need pod exec and a network provider with download policies (cilium)")
	}
	if d.MaxBytes < 0 || d.MaxBytes > download.MaxMaxBytes {
		return fmt.Errorf("DOWNLOAD_MAX_BYTES must be at most %d", download.MaxMaxBytes)
	}
	if d.Timeout < 0 || d.Timeout > download.MaxTimeout {
		return fmt.Errorf("DOWNLOAD_TIMEOUT must be at most %s", download.MaxTimeout)
	}
	d.init()
	return nil
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// downloadOperator admits only a signed-in OIDC operator, like LAN grants.
func downloadOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	method, _ := r.Context().Value(authKey{}).(string)
	actor, _ := r.Context().Value(actorKey{}).(string)
	if method != "oidc" || actor == "" {
		http.Error(w, "downloads need a signed-in OIDC operator; automation and static credentials cannot start them", http.StatusForbidden)
		return "", false
	}
	return actor, true
}

// target is a Ready project and its two pods.
type target struct {
	obj    *api.ProjectWorkspace
	ns     string
	uid    string
	worker string
	proxy  string
}

func (s *Server) downloadTarget(ctx context.Context, name string) (*target, int, string) {
	obj := &api.ProjectWorkspace{}
	if err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, obj); err != nil {
		return nil, http.StatusNotFound, "project unavailable"
	}
	if !obj.DeletionTimestamp.IsZero() || obj.Spec.DesiredState != "Running" || obj.Status.Phase != "Ready" {
		return nil, http.StatusConflict, "the project must be running and Ready"
	}
	ns := obj.Status.Namespace
	if !projectNamespacePattern.MatchString(ns) || obj.UID == "" {
		return nil, http.StatusConflict, "downloads run only in project namespaces (apc-<uid>)"
	}
	t := &target{obj: obj, ns: ns, uid: string(obj.UID)}
	for role, into := range map[string]*string{"worker": &t.worker, "proxy": &t.proxy} {
		var pods corev1.PodList
		if err := s.Client.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{api.OwnerLabel: t.uid, "projects.agentwork.dev/role": role}); err != nil {
			return nil, http.StatusServiceUnavailable, "project pods unavailable"
		}
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp.IsZero() && podReady(&p) {
				if *into != "" {
					return nil, http.StatusConflict, "the project has more than one " + role + " pod; wait for it to settle"
				}
				*into = p.Name
			}
		}
		if *into == "" {
			return nil, http.StatusConflict, "the project " + role + " pod is not ready"
		}
	}
	return t, 0, ""
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (d *Downloads) reserve(uid, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if other, ok := d.busy[uid]; ok {
		return fmt.Errorf("another check or download (%s) is running in this sandbox", other)
	}
	if len(d.busy) >= maxBusy {
		return errors.New("too many downloads are running; try again shortly")
	}
	d.busy[uid] = id
	return nil
}

func (d *Downloads) release(uid string, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy[uid] == id {
		delete(d.busy, uid)
	}
	delete(d.cancel, id)
}

func (d *Downloads) prune() {
	now := d.now()
	for id, c := range d.checks {
		if now.After(c.Expires) && c.State != "checking" {
			delete(d.checks, id)
		}
	}
	byWorkspace := map[string][]*jobRecord{}
	for _, j := range d.jobs {
		byWorkspace[j.uid] = append(byWorkspace[j.uid], j)
	}
	for _, list := range byWorkspace {
		sort.Slice(list, func(a, b int) bool { return list[a].Started.After(list[b].Started) })
		for _, j := range list[min(len(list), keepJobs):] {
			if j.Finished != nil {
				delete(d.jobs, j.ID)
			}
		}
	}
}

// --- HTTP handlers ---

func (s *Server) startDownloadCheck(w http.ResponseWriter, r *http.Request) {
	actor, ok := downloadOperator(w, r)
	if !ok {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if decode(w, r, &req) != nil {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	u, err := download.ParseURL(req.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t, status, msg := s.downloadTarget(r.Context(), r.PathValue("name"))
	if t == nil {
		http.Error(w, msg, status)
		return
	}
	d := s.Downloads
	id := newID()
	if err := d.reserve(t.uid, id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	now := d.now()
	rec := &checkRecord{uid: t.uid, Check: Check{ID: id, Workspace: t.obj.Name, Input: download.Exact(u), State: "checking", Hops: []download.Hop{}, DefaultDestination: download.DefaultDestination(u), RequestedBy: actor, Started: now, Expires: now.Add(checkLifetime)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(download.MaxHops)*2*time.Minute)
	d.mu.Lock()
	d.prune()
	d.checks[id] = rec
	d.cancel[id] = cancel
	d.mu.Unlock()
	slog.Info("download chain check started", "workspace", t.obj.Name, "workspace_uid", t.uid, "check", id, "actor", actor, "url", download.Redact(u))
	go func() {
		defer cancel()
		defer d.release(t.uid, id)
		d.runCheck(ctx, s, t, rec, u)
	}()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, d.checkView(rec))
}

func (s *Server) getDownloadCheck(w http.ResponseWriter, r *http.Request) {
	d := s.Downloads
	d.mu.Lock()
	rec, ok := d.checks[r.PathValue("id")]
	var view Check
	if ok {
		view = d.checkViewLocked(rec)
	}
	d.mu.Unlock()
	if !ok || view.Workspace != r.PathValue("name") {
		http.Error(w, "no such check (checks are kept 15 minutes)", http.StatusNotFound)
		return
	}
	writeJSON(w, view)
}

func (s *Server) startDownload(w http.ResponseWriter, r *http.Request) {
	actor, ok := downloadOperator(w, r)
	if !ok {
		return
	}
	var req struct {
		CheckID            string `json:"checkId"`
		Digest             string `json:"digest"`
		Destination        string `json:"destination"`
		SHA256             string `json:"sha256,omitempty"`
		Overwrite          bool   `json:"overwrite,omitempty"`
		AcceptQueryChanges bool   `json:"acceptQueryChanges,omitempty"`
	}
	if decode(w, r, &req) != nil || !operationID.MatchString(req.CheckID) || req.Digest == "" {
		http.Error(w, "checkId, digest and destination required; check the URL first", http.StatusBadRequest)
		return
	}
	if err := download.CheckDestination(req.Destination); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sha, err := download.CheckSHA256(req.SHA256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	d := s.Downloads
	d.mu.Lock()
	check, ok := d.checks[req.CheckID]
	var hops []download.Hop
	var input string
	if ok {
		hops = append([]download.Hop(nil), check.Hops...)
		input = check.Input
		ok = check.Workspace == r.PathValue("name") && check.State == "ready" && check.Digest == req.Digest && d.now().Before(check.Expires)
	}
	d.mu.Unlock()
	if !ok {
		http.Error(w, "that redirect chain is no longer approved (changed, expired or not checked); check the URL again", http.StatusConflict)
		return
	}
	t, status, msg := s.downloadTarget(r.Context(), r.PathValue("name"))
	if t == nil {
		http.Error(w, msg, status)
		return
	}
	if t.uid != check.uid {
		http.Error(w, "the project was replaced since the check; check the URL again", http.StatusConflict)
		return
	}
	id := newID()
	if err := d.reserve(t.uid, id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	rec := &jobRecord{uid: t.uid, Job: Job{ID: id, Workspace: t.obj.Name, CheckID: req.CheckID, Input: input, State: "starting", Hops: hops, Followed: []string{}, Destination: req.Destination, Overwrite: req.Overwrite, ExpectedSHA256: sha, AcceptQueryChanges: req.AcceptQueryChanges, MaxBytes: d.MaxBytes, Total: -1, RequestedBy: actor, Started: d.now()}}
	if err := s.downloadAudit(r.Context(), t.obj, "download-requested", id, actor, rec.requestedRecord()); err != nil {
		d.release(t.uid, id)
		slog.ErrorContext(r.Context(), "download refused: audit record failed", "workspace_uid", t.uid, "error", err.Error())
		http.Error(w, "the download was not started because its audit record could not be written", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(hops))*(d.Timeout+2*time.Minute))
	d.mu.Lock()
	d.prune()
	d.jobs[id] = rec
	d.cancel[id] = cancel
	// The approved chain is used once; a retry needs a new check.
	delete(d.checks, req.CheckID)
	d.mu.Unlock()
	go func() {
		defer cancel()
		defer d.release(t.uid, id)
		d.runJob(ctx, s, t, rec)
	}()
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, d.jobView(rec))
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	d := s.Downloads
	d.mu.Lock()
	out := []Job{}
	for _, j := range d.jobs {
		if j.Workspace == r.PathValue("name") {
			out = append(out, d.jobViewLocked(j))
		}
	}
	d.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	writeJSON(w, out)
}

func (s *Server) getDownload(w http.ResponseWriter, r *http.Request) {
	d := s.Downloads
	d.mu.Lock()
	j, ok := d.jobs[r.PathValue("id")]
	var view Job
	if ok {
		view = d.jobViewLocked(j)
	}
	d.mu.Unlock()
	if !ok || view.Workspace != r.PathValue("name") {
		http.Error(w, "no such download", http.StatusNotFound)
		return
	}
	writeJSON(w, view)
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := downloadOperator(w, r); !ok {
		return
	}
	var req struct{}
	if decode(w, r, &req) != nil {
		http.Error(w, "empty JSON object required", http.StatusBadRequest)
		return
	}
	d := s.Downloads
	d.mu.Lock()
	j, ok := d.jobs[r.PathValue("id")]
	cancel := d.cancel[r.PathValue("id")]
	ok = ok && j.Workspace == r.PathValue("name")
	d.mu.Unlock()
	if !ok {
		http.Error(w, "no such download", http.StatusNotFound)
		return
	}
	if cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"id": r.PathValue("id")})
}

func (d *Downloads) checkView(rec *checkRecord) Check {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkViewLocked(rec)
}
func (d *Downloads) checkViewLocked(rec *checkRecord) Check {
	v := rec.Check
	v.Hops = append([]download.Hop(nil), rec.Hops...)
	return v
}
func (d *Downloads) jobView(rec *jobRecord) Job {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobViewLocked(rec)
}
func (d *Downloads) jobViewLocked(rec *jobRecord) Job {
	v := rec.Job
	v.Hops = append([]download.Hop(nil), rec.Hops...)
	v.Followed = append([]string(nil), rec.Followed...)
	return v
}

// --- the operations ---

// probeRefused are probe outcomes after which the chain cannot be downloaded.
func (d *Downloads) runCheck(ctx context.Context, s *Server, t *target, rec *checkRecord, u *url.URL) {
	hosts := []string{}
	seen := map[string]bool{}
	prev := ""
	finish := func(state, msg string) {
		d.mu.Lock()
		rec.State, rec.Error = state, msg
		if state == "ready" {
			rec.Digest = download.ChainDigest(rec.Hops)
		}
		rec.Expires = d.now().Add(checkLifetime)
		hops := append([]download.Hop(nil), rec.Hops...)
		d.mu.Unlock()
		data := map[string]string{"url": download.Redact(u), "chain": chainSummary(hops), "outcome": state}
		if msg != "" {
			data["error"] = msg
		}
		actionMsg := fmt.Sprintf("Download redirect chain checked by %s: %s (%s)", rec.RequestedBy, chainSummary(hops), state)
		if err := s.downloadAudit(context.Background(), t.obj, "download-chain-checked", rec.ID, rec.RequestedBy, withMessage(data, actionMsg)); err != nil {
			slog.Error("download check audit record failed", "workspace_uid", t.uid, "check", rec.ID, "error", err.Error())
		}
	}
	defer d.cleanupPolicy(s, t, rec.ID)
	cur := u
	for i := 0; ; i++ {
		hop := download.NewHop(cur, prev)
		if seen[hop.URL] {
			hop.Refused = "redirect loop: this URL appeared earlier in the chain"
			d.appendHop(&rec.Hops, hop)
			finish("failed", "the redirect chain loops")
			return
		}
		if i >= download.MaxHops {
			hop.Refused = fmt.Sprintf("more than %d hops", download.MaxHops)
			d.appendHop(&rec.Hops, hop)
			finish("failed", fmt.Sprintf("more than %d redirects", download.MaxHops-1))
			return
		}
		seen[hop.URL] = true
		hosts = appendHost(hosts, cur.Hostname())
		if err := d.applyPolicy(ctx, s, t, rec.ID, hosts); err != nil {
			d.appendHop(&rec.Hops, hop)
			finish("failed", err.Error())
			return
		}
		st, ev, err := d.grantAndRun(ctx, t, rec.ID+"-"+strconv.Itoa(i), download.Job{Mode: "probe", URL: hop.URL, TimeoutSeconds: 90}, nil)
		hop.Status, hop.Probe, hop.ContentLength, hop.ContentType = st.Status, st.Method, st.ContentLength, st.ContentType
		if err != nil {
			d.appendHop(&rec.Hops, hop)
			finish("failed", err.Error())
			return
		}
		if ev.Event != "probe" {
			d.appendHop(&rec.Hops, hop)
			finish("failed", "the sandbox helper did not report the check")
			return
		}
		if st.Status >= 300 && st.Status < 400 {
			next, rerr := download.ResolveRedirect(cur, st.Location)
			if rerr != nil {
				hop.Refused = rerr.Error()
				d.appendHop(&rec.Hops, hop)
				finish("failed", "a redirect was refused: "+rerr.Error())
				return
			}
			d.appendHop(&rec.Hops, hop)
			prev, cur = cur.Hostname(), next
			continue
		}
		d.appendHop(&rec.Hops, hop)
		if st.Status == http.StatusOK {
			finish("ready", "")
		} else if st.Status == 0 {
			finish("failed", "the destination could not be reached through the sandbox proxy ("+st.Failure+")")
		} else {
			finish("failed", fmt.Sprintf("the server answered %d %s", st.Status, http.StatusText(st.Status)))
		}
		return
	}
}

func (d *Downloads) appendHop(list *[]download.Hop, hop download.Hop) {
	d.mu.Lock()
	*list = append(*list, hop)
	d.mu.Unlock()
}

func appendHost(hosts []string, host string) []string {
	for _, h := range hosts {
		if h == host {
			return hosts
		}
	}
	return append(hosts, host)
}

func (d *Downloads) runJob(ctx context.Context, s *Server, t *target, rec *jobRecord) {
	hosts := []string{}
	for _, h := range rec.Hops {
		hosts = appendHost(hosts, h.Host)
	}
	update := func(f func(j *Job)) {
		d.mu.Lock()
		f(&rec.Job)
		d.mu.Unlock()
	}
	defer d.cleanupPolicy(s, t, rec.ID)
	defer func() {
		d.mu.Lock()
		now := d.now()
		rec.Finished = &now
		if rec.State == "starting" || rec.State == "downloading" {
			rec.State = "failed"
		}
		if ctx.Err() != nil && rec.State == "failed" && rec.Code == "" {
			rec.State, rec.Code, rec.Error = "cancelled", download.CodeCancelled, "download cancelled or timed out"
		}
		record := rec.finishedRecord()
		d.mu.Unlock()
		if err := s.downloadAudit(context.Background(), t.obj, "download-finished", rec.ID, rec.RequestedBy, record); err != nil {
			slog.Error("download audit record failed", "workspace_uid", t.uid, "download", rec.ID, "error", err.Error())
		}
	}()
	if err := d.applyPolicy(ctx, s, t, rec.ID, hosts); err != nil {
		update(func(j *Job) { j.Error = err.Error() })
		return
	}
	cur, _ := download.ParseURL(rec.Hops[0].URL)
	for i := 0; i < len(rec.Hops); i++ {
		update(func(j *Job) { j.Hop, j.State = i, "downloading"; j.Followed = append(j.Followed, download.Redact(cur)) })
		job := download.Job{Mode: "fetch", URL: download.Exact(cur), TimeoutSeconds: int(d.Timeout / time.Second), Home: workerHome, Destination: rec.Destination, SHA256: rec.ExpectedSHA256, Overwrite: rec.Overwrite, MaxBytes: rec.MaxBytes}
		st, ev, err := d.grantAndRun(ctx, t, rec.ID+"-"+strconv.Itoa(i), job, func(e download.Event) {
			if e.Event == "progress" {
				update(func(j *Job) { j.Bytes, j.Total = e.Bytes, e.Total })
			}
		})
		if err != nil {
			update(func(j *Job) { j.Error, j.Code = err.Error(), ev.Code })
			return
		}
		switch ev.Event {
		case "done":
			update(func(j *Job) {
				j.State, j.Bytes, j.Total, j.SHA256, j.Path = "done", ev.Bytes, ev.Bytes, ev.SHA256, ev.Path
				if i < len(j.Hops)-1 {
					j.Note = fmt.Sprintf("the server answered at hop %d of %d without redirecting further; the file came from that approved URL", i+1, len(j.Hops))
				}
			})
			return
		case "redirect":
			next, rerr := download.ResolveRedirect(cur, st.Location)
			if rerr != nil {
				update(func(j *Job) {
					j.State, j.Error = "changed", "a redirect was refused: "+rerr.Error()
					j.Change = &download.Change{Hop: i + 1, Expected: expectedAt(j.Hops, i+1), Observed: download.RedactString(st.Location), Reason: rerr.Error()}
				})
				return
			}
			if i+1 >= len(rec.Hops) {
				update(func(j *Job) {
					j.State, j.Error = "changed", "the server now redirects where it answered directly during the check"
					j.Change = &download.Change{Hop: i + 1, Expected: "(end of the approved chain)", Observed: download.Redact(next), Reason: "extra redirect"}
				})
				return
			}
			want, _ := download.ParseURL(rec.Hops[i+1].URL)
			if download.Exact(next) != download.Exact(want) {
				queryOnly := download.SameExceptQuery(next, want)
				if !(queryOnly && rec.AcceptQueryChanges) {
					update(func(j *Job) {
						j.State, j.Error = "changed", "the redirect chain changed since it was checked; review the new chain"
						reason := "different URL"
						if queryOnly {
							reason = "same host and path, different query (for example a fresh signature)"
						}
						j.Change = &download.Change{Hop: i + 1, Expected: download.Redact(want), Observed: download.Redact(next), QueryOnly: queryOnly, Reason: reason}
					})
					return
				}
			}
			cur = next
		default:
			update(func(j *Job) { j.Error = "the sandbox helper did not report a result" })
			return
		}
	}
}

func expectedAt(hops []download.Hop, i int) string {
	if i < len(hops) {
		return hops[i].Redacted
	}
	return "(end of the approved chain)"
}

// grantAndRun gives the proxy a one-time grant for job.URL and runs the
// helper in the worker. It returns what the proxy observed and the helper's
// final event.
func (d *Downloads) grantAndRun(ctx context.Context, t *target, grantID string, job download.Job, progress func(download.Event)) (proxy.GrantStatus, download.Event, error) {
	token := newToken()
	timeout := job.TimeoutSeconds
	if job.Mode == "probe" {
		timeout = 60
	}
	g := proxy.DownloadGrant{ID: grantID, URL: job.URL, Mode: job.Mode, TokenSHA256: download.TokenHash(token), Expires: d.now().Add(download.GrantTTL).Unix(), MaxBytes: job.MaxBytes, TimeoutSeconds: timeout}
	if _, err := d.proxyCommand(ctx, t, proxy.GrantCommand{Op: "add", Grant: &g}); err != nil {
		return proxy.GrantStatus{}, download.Event{}, err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = d.proxyCommand(c, t, proxy.GrantCommand{Op: "revoke", ID: grantID})
	}()
	job.Token, job.Proxy, job.CAFile = token, workerProxy, workerCAFile
	if job.Home == "" {
		job.Home = workerHome
	}
	line, _ := json.Marshal(job)
	stdinR, stdinW := io.Pipe()
	go func() { _, _ = stdinW.Write(append(line, '\n')) }()
	stdoutR, stdoutW := io.Pipe()
	var stderr limitedBuffer
	var final download.Event
	parsed := make(chan struct{})
	go func() {
		defer close(parsed)
		sc := bufio.NewScanner(stdoutR)
		sc.Buffer(make([]byte, 4096), 64<<10)
		for sc.Scan() {
			var e download.Event
			if json.Unmarshal(sc.Bytes(), &e) != nil {
				continue
			}
			switch e.Event {
			case "progress", "ready":
				if progress != nil {
					progress(e)
				}
			default:
				final = e
			}
		}
		_, _ = io.Copy(io.Discard, stdoutR)
	}()
	execErr := d.Exec(ctx, t.ns, t.worker, "workspace", helperCommand, stdinR, stdoutW, &stderr)
	stdinW.Close()
	stdoutW.Close()
	<-parsed
	st, statusErr := d.proxyStatus(t, grantID)
	if final.Event == "error" {
		return st, final, errors.New(helperMessage(final))
	}
	if final.Event == "" {
		if strings.Contains(stderr.String(), "unknown command") {
			return st, final, errors.New("this sandbox's workspace image predates downloads; it gets the helper when the project restarts on the updated image")
		}
		if ctx.Err() != nil {
			return st, download.Event{Code: download.CodeCancelled}, errors.New("cancelled or timed out")
		}
		if execErr != nil {
			return st, final, errors.New("could not run the download helper in the sandbox")
		}
		return st, final, errors.New("the sandbox helper ended without a result")
	}
	if statusErr != nil {
		return st, final, errors.New("the proxy's record of the request is unavailable")
	}
	return st, final, nil
}

func helperMessage(e download.Event) string {
	if e.Error == "" {
		return "the download failed in the sandbox"
	}
	return e.Error
}

func (d *Downloads) proxyStatus(t *target, id string) (proxy.GrantStatus, error) {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := d.proxyCommand(c, t, proxy.GrantCommand{Op: "status", ID: id})
	var st proxy.GrantStatus
	if err == nil {
		err = json.Unmarshal(raw, &st)
	}
	if err != nil {
		return proxy.GrantStatus{ContentLength: -1}, err
	}
	return st, nil
}

func (d *Downloads) proxyCommand(ctx context.Context, t *target, cmd proxy.GrantCommand) ([]byte, error) {
	line, _ := json.Marshal(cmd)
	var out, errOut limitedBuffer
	err := d.Exec(ctx, t.ns, t.proxy, "proxy", grantCommand, strings.NewReader(string(line)+"\n"), &out, &errOut)
	if err != nil {
		if strings.Contains(errOut.String(), "unknown command") {
			return nil, errors.New("this project's proxy image predates downloads; it gets grant support when the project restarts on the updated image")
		}
		msg := strings.TrimSpace(errOut.String())
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("the sandbox proxy refused the grant: %s", msg)
	}
	return []byte(out.String()), nil
}

func (d *Downloads) applyPolicy(ctx context.Context, s *Server, t *target, id string, hosts []string) error {
	obj := d.Policy.DownloadObject(t.ns, t.uid, id, hosts, d.now().Add(d.Timeout*time.Duration(download.MaxHops)+10*time.Minute))
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(obj.GroupVersionKind())
	err := s.Client.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	switch {
	case apierrors.IsNotFound(err):
		err = s.Client.Create(ctx, obj)
	case err == nil:
		if existing.GetLabels()[network.DownloadLabel] != id {
			return errors.New("a policy with the download's name exists and is not owned by it")
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		err = s.Client.Update(ctx, obj)
	}
	if err != nil {
		return errors.New("could not open the sandbox proxy to the download host")
	}
	settle := d.Settle
	if settle == 0 {
		settle = 2 * time.Second
	}
	select {
	case <-time.After(settle):
	case <-ctx.Done():
		return errors.New("cancelled")
	}
	return nil
}

func (d *Downloads) cleanupPolicy(s *Server, t *target, id string) {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	obj := d.Policy.DownloadObject(t.ns, t.uid, id, nil, time.Time{})
	if err := s.Client.Delete(c, obj); err != nil && !apierrors.IsNotFound(err) {
		slog.Warn("download policy left for the sweeper", "namespace", t.ns, "policy", obj.GetName())
	}
}

// Sweep deletes download policies past their expiry, for example after a
// controller restart interrupted a download, and records the interruption.
func (s *Server) SweepDownloads(ctx context.Context) {
	if s.Downloads == nil {
		return
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schemaCNPList)
	if err := s.Client.List(ctx, list, client.HasLabels{network.DownloadLabel}); err != nil {
		return
	}
	now := s.Downloads.now()
	for i := range list.Items {
		item := &list.Items[i]
		if !strings.HasPrefix(item.GetName(), network.DownloadPolicyPrefix) || !projectNamespacePattern.MatchString(item.GetNamespace()) {
			continue
		}
		expires, err := time.Parse(time.RFC3339, item.GetAnnotations()[network.DownloadExpiresAnnotation])
		if err == nil && now.Before(expires) {
			continue
		}
		if err := s.Client.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
			continue
		}
		slog.Info("expired download policy removed", "namespace", item.GetNamespace(), "policy", item.GetName())
	}
}

// RunDownloadSweeper sweeps once a minute until ctx ends.
func (s *Server) RunDownloadSweeper(ctx context.Context) {
	if s.Downloads == nil {
		return
	}
	s.Downloads.init()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.SweepDownloads(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// --- audit ---

func chainSummary(hops []download.Hop) string {
	parts := []string{}
	for _, h := range hops {
		p := h.Redacted
		if h.Status != 0 {
			p += " [" + strconv.Itoa(h.Status) + "]"
		}
		if h.CrossHost {
			p = "(new host) " + p
		}
		if h.Refused != "" {
			p += " (refused: " + h.Refused + ")"
		}
		parts = append(parts, p)
	}
	s := strings.Join(parts, " -> ")
	if len(s) > 8000 {
		s = s[:8000] + "..."
	}
	return s
}

func withMessage(data map[string]string, msg string) map[string]string {
	data["message"] = msg
	return data
}

func (j *jobRecord) requestedRecord() map[string]string {
	data := map[string]string{"url": j.Hops[0].Redacted, "chain": chainSummary(j.Hops), "destination": j.Destination, "overwrite": strconv.FormatBool(j.Overwrite), "maxBytes": strconv.FormatInt(j.MaxBytes, 10), "acceptQueryChanges": strconv.FormatBool(j.AcceptQueryChanges)}
	if j.ExpectedSHA256 != "" {
		data["expectedSha256"] = j.ExpectedSHA256
	}
	return withMessage(data, fmt.Sprintf("Download into sandbox requested by %s: %s to %s (approved chain of %d)", j.RequestedBy, j.Hops[0].Redacted, j.Destination, len(j.Hops)))
}

func (j *jobRecord) finishedRecord() map[string]string {
	data := map[string]string{"url": j.Hops[0].Redacted, "followed": strings.Join(j.Followed, " -> "), "destination": j.Destination, "outcome": j.State, "bytes": strconv.FormatInt(j.Bytes, 10)}
	if j.SHA256 != "" {
		data["sha256"] = j.SHA256
	}
	if j.Error != "" {
		data["error"] = j.Error
	}
	if j.Change != nil {
		data["change"] = fmt.Sprintf("hop %d: expected %s, observed %s (%s)", j.Change.Hop+1, j.Change.Expected, j.Change.Observed, j.Change.Reason)
	}
	msg := fmt.Sprintf("Download %s: %s to %s", j.State, j.Hops[0].Redacted, j.Destination)
	if j.State == "done" {
		msg += fmt.Sprintf(" (%d bytes, sha256 %s)", j.Bytes, j.SHA256)
	} else if j.Error != "" {
		msg += " (" + j.Error + ")"
	}
	return withMessage(data, msg)
}

// downloadAudit writes an immutable record next to the controller's audit
// ConfigMaps, so it appears in the project's History. URLs are redacted.
func (s *Server) downloadAudit(ctx context.Context, obj *api.ProjectWorkspace, action, id, actor string, data map[string]string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	data["workspace"], data["workspaceUID"], data["action"], data["actor"], data["time"], data["download"] = obj.Name, string(obj.UID), action, actor, now, id
	attrs := []any{"workspace", obj.Name, "workspace_uid", string(obj.UID), "action", action, "id", id, "actor", actor}
	for _, k := range []string{"url", "chain", "followed", "destination", "outcome", "bytes", "sha256", "error", "change"} {
		if v, ok := data[k]; ok {
			attrs = append(attrs, k, v)
		}
	}
	slog.InfoContext(ctx, "download audit", attrs...)
	immutable := true
	name := fmt.Sprintf("audit-%s-%s-%s", stringUID(obj), action, id)
	return s.Client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace, Labels: map[string]string{api.OwnerLabel: string(obj.UID)}}, Immutable: &immutable, Data: data})
}

type limitedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := 64<<10 - len(l.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b = append(l.b, p[:room]...)
	}
	return len(p), nil
}
func (l *limitedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}
