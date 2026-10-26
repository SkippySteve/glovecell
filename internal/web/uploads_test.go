package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
)

// upBox runs the real helper (download.Put) against a temporary filesystem in
// place of the worker, so the backend is tested end to end with the helper.
type upBox struct {
	root     string
	home     string
	mu       sync.Mutex
	cmds     [][]string
	where    []string
	oldImg   bool
	bodyRead *atomic.Int64 // bytes the handler had read from the request when the helper saw its first byte
	firstAt  int64
}

type firstRead struct {
	r    io.Reader
	box  *upBox
	seen bool
}

func (f *firstRead) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if n > 0 && !f.seen && f.box.bodyRead != nil {
		f.seen = true
		f.box.mu.Lock()
		f.box.firstAt = f.box.bodyRead.Load()
		f.box.mu.Unlock()
	}
	return n, err
}

func (b *upBox) exec(ctx context.Context, ns, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b.mu.Lock()
	b.cmds = append(b.cmds, cmd)
	b.where = append(b.where, ns+"/"+pod+"/"+container)
	old := b.oldImg
	b.mu.Unlock()
	if old {
		fmt.Fprintln(stderr, "unknown command")
		return errors.New("exit 1")
	}
	if len(cmd) < 2 || cmd[0] != "/usr/local/bin/agent-project-controller" || cmd[1] != "put" {
		return errors.New("unexpected command")
	}
	o, err := download.ParsePutArgs(cmd[2:])
	if err == nil {
		err = download.Put(o, b.root, &firstRead{r: stdin, box: b}, stdout, nil)
	}
	if err != nil {
		download.EmitError(stdout, err)
		return errors.New("exit 1")
	}
	return nil
}

func uploadFixture(t *testing.T) (*dlEnv, *upBox) {
	t.Helper()
	e := downloadFixture(t)
	root := t.TempDir()
	home := filepath.Join(root, "workspace", "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	box := &upBox{root: root, home: home}
	e.s.Downloads.Exec = box.exec
	return e, box
}

func (e *dlEnv) startUpload(t *testing.T, body string, modify ...func(*http.Request)) (Upload, int, string) {
	t.Helper()
	rec := e.do("POST", "/api/v1/workspaces/project/uploads", body, modify...)
	var u Upload
	json.Unmarshal(rec.Body.Bytes(), &u)
	return u, rec.Code, rec.Body.String()
}

func part(offset int64, body io.Reader, n int64) func(*http.Request) {
	return func(r *http.Request) {
		r.Body = io.NopCloser(body)
		r.ContentLength = n
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("X-Upload-Offset", strconv.FormatInt(offset, 10))
	}
}

func (e *dlEnv) sendPart(id string, offset int64, data []byte, modify ...func(*http.Request)) (int, string) {
	mods := append([]func(*http.Request){part(offset, bytes.NewReader(data), int64(len(data)))}, modify...)
	rec := e.do("PUT", "/api/v1/workspaces/project/uploads/"+id+"/data", "", mods...)
	return rec.Code, rec.Body.String()
}

func (e *dlEnv) waitUpload(t *testing.T, id string) Upload {
	t.Helper()
	for i := 0; i < 1000; i++ {
		rec := e.do("GET", "/api/v1/workspaces/project/uploads/"+id, "")
		var u Upload
		json.Unmarshal(rec.Body.Bytes(), &u)
		if u.Finished != nil && e.idle() {
			return u
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("upload did not finish")
	return Upload{}
}

func noUploadTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if strings.Contains(en.Name(), ".apc-upload-") {
			t.Fatalf("temporary file left behind: %s", en.Name())
		}
	}
}

// countingBody counts how far the handler has read the request body.
type countingBody struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingBody) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n.Add(int64(k))
	return k, err
}

func TestUploadStreamsPartsIntoTheSandbox(t *testing.T) {
	e, box := uploadFixture(t)
	data := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB, the fixture's cap
	sum := sha256.Sum256(data)
	u, code, body := e.startUpload(t, fmt.Sprintf(`{"name":"report.pdf","destination":"~/uploads/report.pdf","size":%d,"sha256":%q}`, len(data), hex.EncodeToString(sum[:])))
	if code != 201 || u.State != "receiving" || u.ChunkBytes != UploadChunkBytes || u.Path != "~/uploads/report.pdf" {
		t.Fatalf("%d %s", code, body)
	}
	// Part one is streamed: when the helper sees its first byte the handler
	// has read at most one copy buffer of the request, not the whole part.
	var read atomic.Int64
	box.bodyRead = &read
	first := int64(768 << 10)
	if code, body := e.sendPart(u.ID, 0, nil, part(0, countingBody{r: bytes.NewReader(data[:first]), n: &read}, first)); code != 200 {
		t.Fatalf("part 1: %d %s", code, body)
	}
	box.mu.Lock()
	firstAt := box.firstAt
	box.mu.Unlock()
	if firstAt <= 0 || firstAt > 128<<10 {
		t.Fatalf("handler read %d bytes before the helper saw the first; the part was buffered", firstAt)
	}
	// A repeated or skipped offset is refused without touching the stream.
	if code, _ := e.sendPart(u.ID, 0, data[:10]); code != 409 {
		t.Fatalf("repeated offset: %d", code)
	}
	if code, body := e.sendPart(u.ID, first, data[first:]); code != 200 || !strings.Contains(body, `"state":"done"`) || !strings.Contains(body, hex.EncodeToString(sum[:])) {
		t.Fatalf("last part: %d %s", code, body)
	}
	got, err := os.ReadFile(filepath.Join(box.home, "uploads", "report.pdf"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("uploaded file differs")
	}
	noUploadTemps(t, filepath.Join(box.home, "uploads"))
	done := e.waitUpload(t, u.ID)
	if done.State != "done" || done.SHA256 != hex.EncodeToString(sum[:]) || done.Written != int64(len(data)) || done.Received != int64(len(data)) {
		t.Fatalf("%+v", done)
	}
	if box.where[0] != dlNamespace+"/project/workspace" {
		t.Fatalf("exec target %v", box.where)
	}
	want := []string{"/usr/local/bin/agent-project-controller", "put", "--home=/workspace/home", "--dest=~/uploads/report.pdf", "--size=1048576", "--max-bytes=1048576", "--sha256=" + hex.EncodeToString(sum[:]), "--overwrite=false"}
	if strings.Join(box.cmds[0], " ") != strings.Join(want, " ") {
		t.Fatalf("helper argv %q", box.cmds[0])
	}
	// Audit: request and outcome with name, size, hash and destination, never content.
	actions := map[string]map[string]string{}
	for _, a := range e.audits(t) {
		actions[a["action"]] = a
		for _, v := range a {
			if strings.Contains(v, "0123456789abcdef0123") {
				t.Fatal("file content in an audit record")
			}
		}
	}
	if actions["upload-requested"]["name"] != "report.pdf" || actions["upload-finished"]["outcome"] != "done" || actions["upload-finished"]["sha256"] != hex.EncodeToString(sum[:]) || actions["upload-finished"]["actor"] != "https://auth.example.com#steve" {
		t.Fatalf("%v", actions)
	}
	// The list shows it.
	rec := e.do("GET", "/api/v1/workspaces/project/uploads", "")
	if !strings.Contains(rec.Body.String(), u.ID) {
		t.Fatal(rec.Body.String())
	}
}

func TestUploadCapAndRequestValidation(t *testing.T) {
	e, box := uploadFixture(t)
	if _, code, body := e.startUpload(t, `{"name":"big","destination":"~/big","size":1048577}`); code != 413 || !strings.Contains(body, "limited to 1048576") {
		t.Fatalf("over the cap: %d %s", code, body)
	}
	for name, body := range map[string]string{
		"outside":   `{"name":"x","destination":"/etc/x","size":1}`,
		"traversal": `{"name":"x","destination":"~/../x","size":1}`,
		"bad sha":   `{"name":"x","destination":"~/x","size":1,"sha256":"abc"}`,
		"negative":  `{"name":"x","destination":"~/x","size":-1}`,
		"unknown":   `{"name":"x","destination":"~/x","size":1,"url":"https://x.example.com"}`,
	} {
		if _, code, _ := e.startUpload(t, body); code != 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if len(box.cmds) != 0 {
		t.Fatal("helper ran for a refused request")
	}
	u, code, body := e.startUpload(t, `{"name":"x","destination":"~/x","size":10}`)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	// A part past the announced size, an empty part and an oversized part are refused.
	if code, _ := e.sendPart(u.ID, 0, make([]byte, 11)); code != 409 {
		t.Fatalf("past the size: %d", code)
	}
	if code, _ := e.sendPart(u.ID, 0, nil); code != 411 {
		t.Fatalf("empty part: %d", code)
	}
	if code, _ := e.sendPart(u.ID, 0, nil, part(0, strings.NewReader(""), maxUploadChunk+1)); code != 411 {
		t.Fatalf("oversized part: %d", code)
	}
	// Another operation in the same sandbox waits.
	if _, code, _ := e.startUpload(t, `{"name":"y","destination":"~/y","size":1}`); code != 409 {
		t.Fatalf("second upload: %d", code)
	}
	if code, _ := e.sendPart(u.ID, 0, []byte("0123456789")); code != 200 {
		t.Fatal(code)
	}
	if b, _ := os.ReadFile(filepath.Join(box.home, "x")); string(b) != "0123456789" {
		t.Fatalf("%q", b)
	}
}

func TestUploadAuthAndCSRF(t *testing.T) {
	e, box := uploadFixture(t)
	bearer := func(r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 32))
	}
	start := `{"name":"x","destination":"~/x","size":3}`
	for name, tc := range map[string]struct {
		modify func(*http.Request)
		status int
	}{
		"automation bearer": {bearer, 403},
		"cross origin":      {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, 403},
		"no origin":         {func(r *http.Request) { r.Header.Del("Origin") }, 403},
		"form post":         {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		"signed out":        {func(r *http.Request) { r.Header.Del("Cookie") }, 401},
	} {
		if _, code, _ := e.startUpload(t, start, tc.modify); code != tc.status {
			t.Errorf("start, %s: %d", name, code)
		}
	}
	u, code, _ := e.startUpload(t, start)
	if code != 201 {
		t.Fatal(code)
	}
	for name, tc := range map[string]struct {
		modify func(*http.Request)
		status int
	}{
		"automation bearer": {bearer, 403},
		"cross origin":      {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") }, 403},
		"no origin":         {func(r *http.Request) { r.Header.Del("Origin") }, 403},
		// What a cross-site form could send: no preflight-free route to the data endpoint.
		"text/plain":    {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		"multipart":     {func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=x") }, 415},
		"no offset hdr": {func(r *http.Request) { r.Header.Del("X-Upload-Offset") }, 415},
	} {
		if code, _ := e.sendPart(u.ID, 0, []byte("abc"), tc.modify); code != tc.status {
			t.Errorf("part, %s: %d", name, code)
		}
	}
	// Reads are OIDC-only too.
	for _, p := range []string{"/api/v1/workspaces/project/uploads", "/api/v1/workspaces/project/uploads/" + u.ID} {
		if rec := e.do("GET", p, "", bearer); rec.Code != 403 {
			t.Errorf("bearer read %s: %d", p, rec.Code)
		}
	}
	// Another project's path cannot reach this upload.
	if rec := e.do("PUT", "/api/v1/workspaces/other/uploads/"+u.ID+"/data", "", part(0, strings.NewReader("abc"), 3)); rec.Code != 404 {
		t.Errorf("other workspace: %d", rec.Code)
	}
	// After all the refused attempts the upload still works.
	if code, body := e.sendPart(u.ID, 0, []byte("abc")); code != 200 || !strings.Contains(body, `"state":"done"`) {
		t.Fatalf("%d %s", code, body)
	}
	if len(box.cmds) != 1 {
		t.Fatalf("helper ran %d times", len(box.cmds))
	}
}

type brokenBody struct {
	data []byte
	sent bool
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, b.data), nil
	}
	return 0, errors.New("client disconnected")
}

func TestUploadAbortsCleanly(t *testing.T) {
	e, box := uploadFixture(t)
	// The browser disconnects in the middle of a part.
	u, code, _ := e.startUpload(t, `{"name":"a","destination":"~/a.bin","size":1000}`)
	if code != 201 {
		t.Fatal(code)
	}
	if code, _ := e.sendPart(u.ID, 0, nil, part(0, &brokenBody{data: make([]byte, 300)}, 1000)); code < 400 {
		t.Fatalf("broken part accepted: %d", code)
	}
	if done := e.waitUpload(t, u.ID); done.State != "cancelled" || !strings.Contains(done.Error, "broke") {
		t.Fatalf("%+v", done)
	}
	if _, err := os.Stat(filepath.Join(box.home, "a.bin")); !os.IsNotExist(err) {
		t.Fatal("partial file kept")
	}
	noUploadTemps(t, box.home)
	// Cancel from the console after one part.
	u, _, _ = e.startUpload(t, `{"name":"b","destination":"~/b.bin","size":20}`)
	if code, _ := e.sendPart(u.ID, 0, make([]byte, 10)); code != 200 {
		t.Fatal(code)
	}
	if rec := e.do("POST", "/api/v1/workspaces/project/uploads/"+u.ID+"/cancel", `{}`); rec.Code != 202 {
		t.Fatal(rec.Code)
	}
	if done := e.waitUpload(t, u.ID); done.State != "cancelled" {
		t.Fatalf("%+v", done)
	}
	if code, _ := e.sendPart(u.ID, 10, make([]byte, 10)); code != 409 {
		t.Fatalf("part after cancel: %d", code)
	}
	noUploadTemps(t, box.home)
	// The browser goes away without a word.
	old := uploadIdle
	uploadIdle = 80 * time.Millisecond
	defer func() { uploadIdle = old }()
	u, _, _ = e.startUpload(t, `{"name":"c","destination":"~/c.bin","size":20}`)
	if done := e.waitUpload(t, u.ID); done.State != "cancelled" || !strings.Contains(done.Error, "no data") {
		t.Fatalf("%+v", done)
	}
	noUploadTemps(t, box.home)
	for _, a := range e.audits(t) {
		if a["action"] == "upload-finished" && a["outcome"] != "cancelled" {
			t.Fatalf("%v", a)
		}
	}
}

func TestUploadHelperRefusalsAndOldImages(t *testing.T) {
	e, box := uploadFixture(t)
	os.WriteFile(filepath.Join(box.home, "exists.txt"), []byte("keep"), 0o644)
	if _, code, body := e.startUpload(t, `{"name":"e","destination":"~/exists.txt","size":3}`); code != 409 || !strings.Contains(body, "tick overwrite") {
		t.Fatalf("%d %s", code, body)
	}
	u, code, _ := e.startUpload(t, `{"name":"e","destination":"~/exists.txt","size":3,"overwrite":true}`)
	if code != 201 {
		t.Fatal(code)
	}
	if code, _ := e.sendPart(u.ID, 0, []byte("new")); code != 200 {
		t.Fatal(code)
	}
	if b, _ := os.ReadFile(filepath.Join(box.home, "exists.txt")); string(b) != "new" {
		t.Fatal("overwrite did not replace")
	}
	// A symlink on the path is refused in the sandbox.
	os.Symlink(t.TempDir(), filepath.Join(box.home, "link"))
	if _, code, body := e.startUpload(t, `{"name":"l","destination":"~/link/x","size":1}`); code != 400 || !strings.Contains(body, "symlink") {
		t.Fatalf("%d %s", code, body)
	}
	// A wrong expected SHA-256 deletes the file at the end.
	u, _, _ = e.startUpload(t, fmt.Sprintf(`{"name":"s","destination":"~/s.bin","size":3,"sha256":%q}`, strings.Repeat("0", 64)))
	if code, body := e.sendPart(u.ID, 0, []byte("abc")); code != 400 || !strings.Contains(body, "mismatch") {
		t.Fatalf("%d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(box.home, "s.bin")); !os.IsNotExist(err) {
		t.Fatal("mismatched file kept")
	}
	// An empty file needs no parts.
	if u, code, body := e.startUpload(t, `{"name":"empty","destination":"~/empty","size":0}`); code != 201 || u.State != "done" {
		t.Fatalf("%d %s", code, body)
	}
	// A workspace image without `put` says so plainly.
	box.oldImg = true
	if _, code, body := e.startUpload(t, `{"name":"o","destination":"~/o","size":1}`); code != 501 || !strings.Contains(body, "image too old") {
		t.Fatalf("%d %s", code, body)
	}
	noUploadTemps(t, box.home)
}

func TestSessionAdvertisesUploads(t *testing.T) {
	e, _ := uploadFixture(t)
	rec := e.do("GET", "/api/v1/session", "")
	if !strings.Contains(rec.Body.String(), `"uploads":true`) || !strings.Contains(rec.Body.String(), `"uploadChunkBytes":8388608`) {
		t.Fatal(rec.Body.String())
	}
}
