package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/proxy"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeRoute is how a destination answers one exact URL.
type fakeRoute struct {
	status   int
	location func() string
	body     string
	headFail bool // answers HEAD with 405, like servers without HEAD
}

// fakeSandbox stands in for the project's proxy and worker behind Exec: it
// keeps the proxy's one-time grants and answers the helper's jobs only when
// they present a matching grant, as the real proxy does.
type fakeSandbox struct {
	mu      sync.Mutex
	grants  map[string]proxy.DownloadGrant
	status  map[string]proxy.GrantStatus
	routes  map[string]fakeRoute
	files   map[string]string
	worker  []string // URLs the worker requested, in order
	execNS  []string
	oldImg  bool
	revoked int
}

func newFakeSandbox() *fakeSandbox {
	return &fakeSandbox{grants: map[string]proxy.DownloadGrant{}, status: map[string]proxy.GrantStatus{}, routes: map[string]fakeRoute{}, files: map[string]string{}}
}

func (f *fakeSandbox) exec(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execNS = append(f.execNS, ns+"/"+pod+"/"+container)
	if f.oldImg {
		fmt.Fprintln(stderr, "unknown command")
		return fmt.Errorf("exit 1")
	}
	switch container {
	case "proxy":
		var c proxy.GrantCommand
		if json.Unmarshal([]byte(line), &c) != nil {
			return fmt.Errorf("bad command")
		}
		switch c.Op {
		case "add":
			if _, dup := f.grants[c.Grant.ID]; dup {
				fmt.Fprintln(stderr, "grant id already used")
				return fmt.Errorf("exit 1")
			}
			f.grants[c.Grant.ID] = *c.Grant
			f.status[c.Grant.ID] = proxy.GrantStatus{ID: c.Grant.ID, ContentLength: -1}
			fmt.Fprintf(stdout, `{"id":%q}`, c.Grant.ID)
		case "status":
			st, ok := f.status[c.ID]
			if !ok {
				return fmt.Errorf("404")
			}
			json.NewEncoder(stdout).Encode(st)
		case "revoke":
			delete(f.grants, c.ID)
			f.revoked++
		}
		return nil
	case "workspace":
		var job download.Job
		if json.Unmarshal([]byte(line), &job) != nil {
			return fmt.Errorf("bad job")
		}
		emit := func(e download.Event) { b, _ := json.Marshal(e); stdout.Write(append(b, '\n')) }
		var id string
		for gid, g := range f.grants {
			if g.TokenSHA256 == download.TokenHash(job.Token) {
				id = gid
			}
		}
		g, ok := f.grants[id]
		if !ok || g.URL != job.URL || g.Mode != job.Mode || f.status[id].Used || job.Proxy != "http://proxy:3128" || job.CAFile != "/trust/ca.crt" {
			emit(download.Event{Event: "error", Code: "network", Error: "proxy refused the request"})
			return fmt.Errorf("exit 1")
		}
		f.worker = append(f.worker, job.Mode+" "+job.URL)
		route, ok := f.routes[job.URL]
		if !ok {
			route = fakeRoute{status: 404}
		}
		st := proxy.GrantStatus{ID: id, Used: true, Done: true, Status: route.status, ContentLength: -1, Method: "GET"}
		if job.Mode == "probe" {
			st.Method = "HEAD"
			if route.headFail {
				st.Method = "GET"
			}
		}
		if route.location != nil {
			u, _ := url.Parse(job.URL)
			ref, _ := url.Parse(route.location())
			st.Location = u.ResolveReference(ref).String()
		}
		if route.status == 200 {
			st.ContentLength, st.ContentType = int64(len(route.body)), "application/octet-stream"
		}
		if job.Mode == "fetch" && route.status == 200 {
			st.Bytes = int64(len(route.body))
		}
		f.status[id] = st
		switch {
		case job.Mode == "probe":
			emit(download.Event{Event: "probe", Status: route.status})
		case route.status >= 300 && route.status < 400:
			emit(download.Event{Event: "redirect", Status: route.status})
		case route.status == 200:
			sum := sha256.Sum256([]byte(route.body))
			got := hex.EncodeToString(sum[:])
			if job.SHA256 != "" && job.SHA256 != got {
				emit(download.Event{Event: "error", Code: download.CodeChecksum, Error: "SHA-256 mismatch: got " + got + "; the file was deleted"})
				return fmt.Errorf("exit 1")
			}
			if _, exists := f.files[job.Destination]; exists && !job.Overwrite {
				emit(download.Event{Event: "error", Code: download.CodeExists, Error: "a file already exists at the destination"})
				return fmt.Errorf("exit 1")
			}
			emit(download.Event{Event: "progress", Bytes: int64(len(route.body)) / 2, Total: int64(len(route.body))})
			f.files[job.Destination] = route.body
			emit(download.Event{Event: "done", Bytes: int64(len(route.body)), SHA256: got, Path: job.Destination})
		default:
			emit(download.Event{Event: "error", Code: download.CodeStatus, Error: fmt.Sprintf("server answered %d", route.status)})
			return fmt.Errorf("exit 1")
		}
		return nil
	}
	return fmt.Errorf("unexpected container %s", container)
}

var cnpGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

const dlNamespace = "apc-0123456789abcdef0123456789abcdef"

type dlEnv struct {
	*lanEnv
	box *fakeSandbox
}

func downloadFixture(t *testing.T) *dlEnv {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	scheme.AddKnownTypeWithName(cnpGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(cnpGVK.GroupVersion().WithKind("CiliumNetworkPolicyList"), &unstructured.UnstructuredList{})
	obj := &api.ProjectWorkspace{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "control", UID: types.UID("fixture-uid"), Generation: 1}, Spec: api.WorkspaceSpec{Repository: "steve/repo", DesiredState: "Running"}, Status: api.WorkspaceStatus{Phase: "Ready", Namespace: dlNamespace}}
	ready := corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}
	worker := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: dlNamespace, Labels: map[string]string{api.OwnerLabel: "fixture-uid", "projects.agentwork.dev/role": "worker"}}, Status: ready}
	px := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "proxy-1", Namespace: dlNamespace, Labels: map[string]string{api.OwnerLabel: "fixture-uid", "projects.agentwork.dev/role": "proxy"}}, Status: ready}
	oidc := &OIDC{sessions: map[string]session{"operator-session": {Subject: "https://auth.example.com#steve", Expires: time.Now().Add(time.Hour)}}}
	box := newFakeSandbox()
	s := &Server{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj, worker, px).Build(), Host: host{}, Namespace: "control", Owner: "steve", Password: strings.Repeat("a", 32), Origin: lanOrigin, GitOrigin: "https://git.example.com", OIDC: oidc}
	s.Downloads = &Downloads{Exec: box.exec, Policy: network.Cilium{}, Settle: time.Millisecond, MaxBytes: 1 << 20}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	env := &dlEnv{lanEnv: &lanEnv{s: s, h: h, obj: obj}, box: box}
	return env
}

func (e *dlEnv) check(t *testing.T, raw string) Check {
	t.Helper()
	rec := e.do("POST", "/api/v1/workspaces/project/download-checks", fmt.Sprintf(`{"url":%q}`, raw))
	if rec.Code != 202 {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	var c Check
	json.Unmarshal(rec.Body.Bytes(), &c)
	return e.waitCheck(t, c.ID)
}

func (e *dlEnv) waitCheck(t *testing.T, id string) Check {
	t.Helper()
	for i := 0; i < 500; i++ {
		rec := e.do("GET", "/api/v1/workspaces/project/download-checks/"+id, "")
		var c Check
		json.Unmarshal(rec.Body.Bytes(), &c)
		if c.State != "checking" && e.idle() {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("check did not finish")
	return Check{}
}

func (e *dlEnv) idle() bool {
	e.s.Downloads.mu.Lock()
	defer e.s.Downloads.mu.Unlock()
	return len(e.s.Downloads.busy) == 0
}

func (e *dlEnv) download(t *testing.T, c Check, extra string) Job {
	t.Helper()
	rec := e.do("POST", "/api/v1/workspaces/project/downloads", fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/downloads/tool.tgz"%s}`, c.ID, c.Digest, extra))
	if rec.Code != 202 {
		t.Fatalf("download: %d %s", rec.Code, rec.Body)
	}
	var j Job
	json.Unmarshal(rec.Body.Bytes(), &j)
	for i := 0; i < 500; i++ {
		rec := e.do("GET", "/api/v1/workspaces/project/downloads/"+j.ID, "")
		json.Unmarshal(rec.Body.Bytes(), &j)
		if j.Finished != nil && e.idle() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("download did not finish")
	return Job{}
}

func (e *dlEnv) audits(t *testing.T) []map[string]string {
	var list corev1.ConfigMapList
	if err := e.s.Client.List(context.Background(), &list, client.InNamespace("control")); err != nil {
		t.Fatal(err)
	}
	out := []map[string]string{}
	for _, cm := range list.Items {
		if strings.HasPrefix(cm.Name, "audit-") {
			out = append(out, cm.Data)
		}
	}
	return out
}

func (e *dlEnv) policies(t *testing.T) int {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schemaCNPList)
	if err := e.s.Client.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

const (
	releaseURL = "https://github.com/o/r/releases/download/v1/tool.tgz"
	objectURL  = "https://objects.example.com/o/r/tool.tgz"
)

func githubLike(box *fakeSandbox, sig *int) {
	box.routes[releaseURL] = fakeRoute{status: 302, location: func() string { return fmt.Sprintf("%s?sig=%d&token=secret-%d", objectURL, *sig, *sig) }}
	for i := 0; i < 5; i++ {
		box.routes[fmt.Sprintf("%s?sig=%d&token=secret-%d", objectURL, i, i)] = fakeRoute{status: 200, body: "tool bytes", headFail: true}
	}
}

func TestDownloadCheckShowsTheRedirectChain(t *testing.T) {
	e := downloadFixture(t)
	sig := 0
	githubLike(e.box, &sig)
	c := e.check(t, releaseURL)
	if c.State != "ready" || len(c.Hops) != 2 || c.Digest == "" {
		t.Fatalf("%+v", c)
	}
	first, last := c.Hops[0], c.Hops[1]
	if first.Status != 302 || first.CrossHost || first.Host != "github.com" || first.Probe != "HEAD" {
		t.Fatalf("hop 1 %+v", first)
	}
	if !last.CrossHost || last.Host != "objects.example.com" || last.Status != 200 || last.ContentLength != 10 || last.Probe != "GET" {
		t.Fatalf("hop 2 %+v", last)
	}
	// The operator's view carries the full URL; the redacted form hashes the query.
	if !strings.Contains(last.URL, "token=secret-0") || strings.Contains(last.Redacted, "secret") || last.QueryHash == "" {
		t.Fatalf("query handling %+v", last)
	}
	if c.DefaultDestination != "~/downloads/tool.tgz" {
		t.Fatal(c.DefaultDestination)
	}
	// Exec only in the project's namespace; every grant revoked, the proxy policy removed.
	for _, target := range e.box.execNS {
		if !strings.HasPrefix(target, dlNamespace+"/") {
			t.Fatalf("exec outside the project namespace: %s", target)
		}
	}
	if len(e.box.grants) != 0 || e.policies(t) != 0 {
		t.Fatalf("leftover grants %d or policies %d", len(e.box.grants), e.policies(t))
	}
	audits := e.audits(t)
	if len(audits) != 1 || audits[0]["action"] != "download-chain-checked" || strings.Contains(fmt.Sprint(audits[0]), "secret-0") || audits[0]["actor"] != "https://auth.example.com#steve" {
		t.Fatalf("%v", audits)
	}
}

func TestDownloadCheckRefusesLoopsDowngradesAndLongChains(t *testing.T) {
	e := downloadFixture(t)
	e.box.routes["https://a.example.com/x"] = fakeRoute{status: 302, location: func() string { return "https://b.example.com/y" }}
	e.box.routes["https://b.example.com/y"] = fakeRoute{status: 301, location: func() string { return "https://a.example.com/x" }}
	c := e.check(t, "https://a.example.com/x")
	if c.State != "failed" || !strings.Contains(c.Error, "loops") || len(c.Hops) != 3 || c.Hops[2].Refused == "" {
		t.Fatalf("loop %+v", c)
	}
	e.box.routes["https://a.example.com/down"] = fakeRoute{status: 302, location: func() string { return "http://b.example.com/plain" }}
	c = e.check(t, "https://a.example.com/down")
	// The looping URL was refused before a third probe; the http target is never probed.
	if c.State != "failed" || !strings.Contains(c.Hops[0].Refused, "downgrade") || len(e.box.worker) != 3 {
		t.Fatalf("downgrade %+v %v", c, e.box.worker)
	}
	e.box.routes["https://a.example.com/ip"] = fakeRoute{status: 302, location: func() string { return "https://10.0.0.5/internal" }}
	if c = e.check(t, "https://a.example.com/ip"); c.State != "failed" || c.Hops[0].Refused == "" {
		t.Fatalf("ip %+v", c)
	}
	for i := 0; i < 12; i++ {
		next := fmt.Sprintf("https://c.example.com/%d", i+1)
		e.box.routes[fmt.Sprintf("https://c.example.com/%d", i)] = fakeRoute{status: 302, location: func() string { return next }}
	}
	c = e.check(t, "https://c.example.com/0")
	if c.State != "failed" || len(c.Hops) != download.MaxHops+1 || !strings.Contains(c.Error, "more than") {
		t.Fatalf("cap %d %+v", len(c.Hops), c.Error)
	}
	if n := strings.Count(strings.Join(e.box.worker, ","), "c.example.com"); n != download.MaxHops {
		t.Fatalf("probed %d hops, cap %d", n, download.MaxHops)
	}
	e.box.routes["https://a.example.com/missing"] = fakeRoute{status: 404}
	if c = e.check(t, "https://a.example.com/missing"); c.State != "failed" || !strings.Contains(c.Error, "404") {
		t.Fatalf("404 %+v", c)
	}
}

func TestDownloadFollowsOnlyTheApprovedChain(t *testing.T) {
	e := downloadFixture(t)
	sig := 0
	githubLike(e.box, &sig)
	c := e.check(t, releaseURL)
	j := e.download(t, c, "")
	if j.State != "done" || j.Bytes != 10 || j.SHA256 == "" || e.box.files["~/downloads/tool.tgz"] != "tool bytes" {
		t.Fatalf("%+v", j)
	}
	want := []string{"probe " + releaseURL, "probe " + objectURL + "?sig=0&token=secret-0", "fetch " + releaseURL, "fetch " + objectURL + "?sig=0&token=secret-0"}
	if strings.Join(e.box.worker, ",") != strings.Join(want, ",") {
		t.Fatalf("worker requests %v", e.box.worker)
	}
	// The approved chain is spent.
	rec := e.do("POST", "/api/v1/workspaces/project/downloads", fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/b"}`, c.ID, c.Digest))
	if rec.Code != 409 {
		t.Fatalf("reused check: %d", rec.Code)
	}
	var actions []string
	for _, a := range e.audits(t) {
		actions = append(actions, a["action"])
		if strings.Contains(fmt.Sprint(a), "secret-0") {
			t.Fatalf("query string in audit: %v", a)
		}
		if a["action"] == "download-finished" && (a["outcome"] != "done" || a["sha256"] != j.SHA256 || a["bytes"] != "10" || a["destination"] != "~/downloads/tool.tgz") {
			t.Fatalf("%v", a)
		}
	}
	if !strings.Contains(strings.Join(actions, ","), "download-requested") || !strings.Contains(strings.Join(actions, ","), "download-finished") {
		t.Fatal(actions)
	}
	if e.policies(t) != 0 || len(e.box.grants) != 0 {
		t.Fatal("grant or policy left behind")
	}
}

func TestDownloadStopsWhenTheChainChanges(t *testing.T) {
	e := downloadFixture(t)
	sig := 0
	githubLike(e.box, &sig)
	c := e.check(t, releaseURL)
	sig = 1 // a fresh signature on every request, as signed CDN links do
	j := e.download(t, c, "")
	if j.State != "changed" || j.Change == nil || !j.Change.QueryOnly || j.Change.Hop != 1 || strings.Contains(j.Change.Observed, "secret") {
		t.Fatalf("%+v %+v", j, j.Change)
	}
	if _, wrote := e.box.files["~/downloads/tool.tgz"]; wrote {
		t.Fatal("changed chain was followed")
	}
	// Re-approve: a new check, then accept query-only changes on the same host and path.
	c = e.check(t, releaseURL)
	sig = 2
	j = e.download(t, c, `,"acceptQueryChanges":true`)
	if j.State != "done" || len(j.Followed) != 2 || strings.Contains(strings.Join(j.Followed, ""), "secret") {
		t.Fatalf("%+v", j)
	}
	// A different host is never accepted silently, even with query changes allowed.
	e.box.routes[releaseURL] = fakeRoute{status: 302, location: func() string { return "https://mirror.example.net/tool.tgz" }}
	e.box.routes["https://mirror.example.net/tool.tgz"] = fakeRoute{status: 200, body: "other"}
	sig = 3
	c2 := e.check(t, releaseURL)
	if c2.Hops[1].Host != "mirror.example.net" {
		t.Fatal(c2.Hops)
	}
	e.box.routes[releaseURL] = fakeRoute{status: 302, location: func() string { return "https://evil.example.org/tool.tgz" }}
	j = e.download(t, c2, `,"acceptQueryChanges":true,"overwrite":true`)
	if j.State != "changed" || j.Change.QueryOnly || !strings.Contains(j.Change.Observed, "evil.example.org") {
		t.Fatalf("%+v", j.Change)
	}
	for _, r := range e.box.worker {
		if strings.Contains(r, "evil.example.org") {
			t.Fatal("unapproved host fetched")
		}
	}
}

func TestDownloadRequestValidationAndAuth(t *testing.T) {
	e := downloadFixture(t)
	e.box.routes["https://files.example.com/a.bin"] = fakeRoute{status: 200, body: "abc"}
	c := e.check(t, "https://files.example.com/a.bin")
	for name, tc := range map[string]struct {
		body   string
		status int
		modify []func(*http.Request)
	}{
		"digest mismatch": {fmt.Sprintf(`{"checkId":%q,"digest":"x","destination":"~/a"}`, c.ID), 409, nil},
		"bad destination": {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"/etc/cron.d/x"}`, c.ID, c.Digest), 400, nil},
		"traversal":       {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/../../etc/x"}`, c.ID, c.Digest), 400, nil},
		"bad sha":         {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/a","sha256":"xyz"}`, c.ID, c.Digest), 400, nil},
		"unknown field":   {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/a","url":"https://x.example.com"}`, c.ID, c.Digest), 400, nil},
		"bearer": {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/a"}`, c.ID, c.Digest), 403, []func(*http.Request){func(r *http.Request) {
			r.Header.Del("Cookie")
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
		}}},
		"cross origin": {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/a"}`, c.ID, c.Digest), 403, []func(*http.Request){func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }}},
		"no origin":    {fmt.Sprintf(`{"checkId":%q,"digest":%q,"destination":"~/a"}`, c.ID, c.Digest), 403, []func(*http.Request){func(r *http.Request) { r.Header.Del("Origin") }}},
		"form post":    {"checkId=x", 415, []func(*http.Request){func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }}},
	} {
		rec := e.do("POST", "/api/v1/workspaces/project/downloads", tc.body, tc.modify...)
		if rec.Code != tc.status {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	for raw, want := range map[string]int{"http://files.example.com/a": 400, "https://10.1.1.1/a": 400, "https://files.example.com/a#x": 400} {
		if rec := e.do("POST", "/api/v1/workspaces/project/download-checks", fmt.Sprintf(`{"url":%q}`, raw)); rec.Code != want {
			t.Errorf("%s: %d", raw, rec.Code)
		}
	}
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	if rec := e.do("POST", "/api/v1/workspaces/project/download-checks", `{"url":"https://files.example.com/a.bin"}`, bearer); rec.Code != 403 {
		t.Fatalf("automation bearer started a check: %d", rec.Code)
	}
	// The original approval still works after all the refused attempts.
	if j := e.download(t, c, ""); j.State != "done" {
		t.Fatalf("%+v", j)
	}
}

func TestDownloadReportsHelperFailuresAndOldImages(t *testing.T) {
	e := downloadFixture(t)
	e.box.routes["https://files.example.com/a.bin"] = fakeRoute{status: 200, body: "abc"}
	c := e.check(t, "https://files.example.com/a.bin")
	j := e.download(t, c, `,"sha256":"`+strings.Repeat("0", 64)+`"`)
	if j.State != "failed" || !strings.Contains(j.Error, "mismatch") {
		t.Fatalf("%+v", j)
	}
	e.box.oldImg = true
	rec := e.do("POST", "/api/v1/workspaces/project/download-checks", `{"url":"https://files.example.com/a.bin"}`)
	var started Check
	json.Unmarshal(rec.Body.Bytes(), &started)
	c = e.waitCheck(t, started.ID)
	if c.State != "failed" || !strings.Contains(c.Error, "predates downloads") {
		t.Fatalf("%+v", c)
	}
}

func TestDownloadNeedsAReadyProjectNamespace(t *testing.T) {
	e := downloadFixture(t)
	e.obj.Status.Namespace = "agent-secrets"
	if err := e.s.Client.Status().Update(context.Background(), e.obj); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("POST", "/api/v1/workspaces/project/download-checks", `{"url":"https://files.example.com/a.bin"}`); rec.Code != 409 || len(e.box.execNS) != 0 {
		t.Fatalf("%d %v", rec.Code, e.box.execNS)
	}
}

func TestDownloadSweeperRemovesExpiredPolicies(t *testing.T) {
	e := downloadFixture(t)
	old := network.Cilium{}.DownloadObject(dlNamespace, "fixture-uid", "aaaaaaaaaaaa", []string{"files.example.com"}, time.Now().Add(-time.Minute))
	live := network.Cilium{}.DownloadObject(dlNamespace, "fixture-uid", "bbbbbbbbbbbb", []string{"files.example.com"}, time.Now().Add(time.Hour))
	for _, o := range []*unstructured.Unstructured{old, live} {
		if err := e.s.Client.Create(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
	e.s.SweepDownloads(context.Background())
	if e.policies(t) != 1 {
		t.Fatal("sweeper did not remove exactly the expired policy")
	}
}

func TestDownloadPolicySelectsOnlyTheProxyAndExactHosts(t *testing.T) {
	obj := network.Cilium{}.DownloadObject(dlNamespace, "uid", "abc", []string{"github.com", "objects.example.com"}, time.Now())
	raw, _ := json.Marshal(obj.Object)
	s := string(raw)
	if !strings.Contains(s, `"projects.agentwork.dev/role":"proxy"`) || strings.Contains(s, "worker") || strings.Contains(s, "matchPattern") || strings.Contains(s, "toCIDR") || !strings.Contains(s, `"port":"443"`) {
		t.Fatal(s)
	}
}

func TestSessionAdvertisesDownloads(t *testing.T) {
	e := downloadFixture(t)
	rec := e.do("GET", "/api/v1/session", "")
	if !strings.Contains(rec.Body.String(), `"downloads":true`) || !strings.Contains(rec.Body.String(), `"downloadMaxBytes":1048576`) {
		t.Fatal(rec.Body.String())
	}
	if rec := httptest.NewRecorder(); rec != nil {
		plain := lanFixture(t, false)
		r := plain.do("POST", "/api/v1/workspaces/project/download-checks", `{"url":"https://files.example.com/a"}`)
		if r.Code != 404 && r.Code != 405 {
			t.Fatalf("downloads route without the feature: %d", r.Code)
		}
	}
}
