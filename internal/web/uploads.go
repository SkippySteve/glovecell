package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	api "git.cluster.aimingupward.com/steve/agent-project-controller/api/v1alpha1"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/download"
)

// Uploads ("Upload into sandbox") send a file from the operator's browser into
// a project sandbox. The browser reads the file locally (it may have been
// downloaded with the operator's own logged-in session; no credential leaves
// the browser) and sends it in parts:
//
//  1. POST .../uploads {name, destination, size, sha256, overwrite}: the
//     controller starts `agent-project-controller put` in the worker (exec, as
//     the worker's user) and answers once the helper has accepted the
//     destination, so a refused path costs no upload.
//  2. PUT .../uploads/<id>/data with X-Upload-Offset, one part of at most
//     UploadChunkBytes each, in order. Each part streams straight into the
//     helper's stdin through a 64 KiB buffer; nothing is held in memory or on
//     the controller's disk. Parts keep every request short, so the ingress
//     read timeout (60 s on this cluster) never sees a long request.
//  3. After the last part the helper verifies the size and the optional
//     SHA-256, moves the file into place and reports its SHA-256.
//
// A broken part, a cancel, or no data for two minutes ends the helper's stdin
// early; the helper then removes its temporary file and keeps nothing.
const (
	UploadChunkBytes = 8 << 20
	maxUploadChunk   = 16 << 20
	uploadLifetime   = download.MaxTimeout
	uploadReadyWait  = time.Minute
	uploadFinishWait = 2 * time.Minute
	chunkDeadline    = 2 * time.Minute
	keepUploads      = 20
)

// uploadIdle ends an upload whose browser stopped sending (a variable for tests).
var uploadIdle = 2 * time.Minute

var uploadDataPath = regexp.MustCompile(`^/api/v1/workspaces/[a-z][a-z0-9-]{0,39}/uploads/[a-z0-9]{12}/data$`)

var errUploadAborted = errors.New("upload aborted")
var errHelperEnded = errors.New("the sandbox helper ended")

// Upload is the console's view of one upload.
type Upload struct {
	ID             string      `json:"id"`
	Workspace      string      `json:"workspace"`
	Name           string      `json:"name"`
	Destination    string      `json:"destination"`
	Size           int64       `json:"size"`
	Received       int64       `json:"received"`
	Written        int64       `json:"written"`
	Overwrite      bool        `json:"overwrite"`
	ExpectedSHA256 string      `json:"expectedSha256,omitempty"`
	SHA256         string      `json:"sha256,omitempty"`
	Path           string      `json:"path,omitempty"`
	State          uploadState `json:"state"`
	Code           string      `json:"code,omitempty"`
	Error          string      `json:"error,omitempty"`
	ChunkBytes     int64       `json:"chunkBytes"`
	MaxBytes       int64       `json:"maxBytes"`
	RequestedBy    string      `json:"requestedBy"`
	Started        time.Time   `json:"started"`
	Finished       *time.Time  `json:"finished,omitempty"`
}

type uploadRecord struct {
	Upload
	uid      string
	chunk    sync.Mutex
	stdin    *io.PipeWriter
	lastData time.Time
	aborted  string
	ready    chan struct{}
	ended    chan struct{}
	once     sync.Once
	cancel   context.CancelFunc
}

func (u *uploadRecord) markReady() { u.once.Do(func() { close(u.ready) }) }

// CodeTooOld marks a workspace image whose helper has no `put`.
const CodeTooOld = "too-old"

func (d *Downloads) uploadView(rec *uploadRecord) Upload {
	d.mu.Lock()
	defer d.mu.Unlock()
	return rec.Upload
}

func (d *Downloads) pruneUploads() {
	by := map[string][]*uploadRecord{}
	for _, u := range d.uploads {
		by[u.uid] = append(by[u.uid], u)
	}
	for _, list := range by {
		sort.Slice(list, func(a, b int) bool { return list[a].Started.After(list[b].Started) })
		for _, u := range list[min(len(list), keepUploads):] {
			if u.Finished != nil {
				delete(d.uploads, u.ID)
			}
		}
	}
}

// displayName keeps the browser's file name readable in History and logs.
func displayName(raw, destination string) string {
	if raw == "" {
		raw = path.Base(destination)
	}
	raw = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	if len(raw) > 255 {
		raw = raw[:255]
	}
	return raw
}

func (s *Server) startUpload(w http.ResponseWriter, r *http.Request) {
	actor, ok := downloadOperator(w, r)
	if !ok {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Destination string `json:"destination"`
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256,omitempty"`
		Overwrite   bool   `json:"overwrite,omitempty"`
	}
	if decode(w, r, &req) != nil {
		http.Error(w, "name, destination and size required", http.StatusBadRequest)
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
	if req.Size < 0 {
		http.Error(w, "size must not be negative", http.StatusBadRequest)
		return
	}
	if req.Size > d.MaxBytes {
		http.Error(w, fmt.Sprintf("the file is %d bytes; uploads are limited to %d bytes", req.Size, d.MaxBytes), http.StatusRequestEntityTooLarge)
		return
	}
	t, status, msg := s.projectTarget(r.Context(), r.PathValue("name"), "worker")
	if t == nil {
		http.Error(w, msg, status)
		return
	}
	id := newID()
	if err := d.reserve(t.uid, id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	rec := &uploadRecord{uid: t.uid, ready: make(chan struct{}), ended: make(chan struct{}), Upload: Upload{ID: id, Workspace: t.obj.Name, Name: displayName(req.Name, req.Destination),
		Destination: req.Destination, Size: req.Size, Overwrite: req.Overwrite, ExpectedSHA256: sha, State: uploadStarting, ChunkBytes: UploadChunkBytes, MaxBytes: d.MaxBytes, RequestedBy: actor, Started: d.now()}}
	if err := s.uploadAudit(r.Context(), t.obj, "upload-requested", rec); err != nil {
		d.release(t.uid, id)
		slog.ErrorContext(r.Context(), "upload refused: audit record failed", "workspace_uid", t.uid, "error", err.Error())
		http.Error(w, "the upload was not started because its audit record could not be written", http.StatusServiceUnavailable)
		return
	}
	stdinR, stdinW := io.Pipe()
	rec.stdin = stdinW
	rec.lastData = d.now()
	ctx, cancel := context.WithTimeout(context.Background(), uploadLifetime)
	rec.cancel = cancel
	d.mu.Lock()
	d.pruneUploads()
	d.uploads[id] = rec
	d.mu.Unlock()
	go func() {
		defer cancel()
		d.runUpload(ctx, s, t, rec, stdinR)
	}()
	select {
	case <-rec.ready:
	case <-time.After(uploadReadyWait):
		d.abortUpload(rec, "the sandbox helper did not start in time")
		<-rec.ended
	}
	view := d.uploadView(rec)
	if view.State == uploadReceiving && view.Size == 0 {
		view = d.finishUpload(rec)
	}
	if view.State == uploadFailed || view.State == uploadCancelled {
		http.Error(w, view.Error, uploadErrorStatus(view.Code))
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, view)
}

func uploadErrorStatus(code string) int {
	switch code {
	case download.CodeExists:
		return http.StatusConflict
	case download.CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case download.CodeInvalid, download.CodeOutside, download.CodeSymlink, download.CodeNotRegular, download.CodeChecksum:
		return http.StatusBadRequest
	case CodeTooOld:
		return http.StatusNotImplemented
	}
	return http.StatusBadGateway
}

// runUpload runs the helper until it reports a result or its stdin ends.
func (d *Downloads) runUpload(ctx context.Context, s *Server, t *target, rec *uploadRecord, stdin *io.PipeReader) {
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
			case "ready":
				d.mu.Lock()
				rec.State, rec.Path = rec.State.to(uploadReceiving), e.Path
				d.mu.Unlock()
				rec.markReady()
			case "progress":
				d.mu.Lock()
				rec.Written = e.Bytes
				d.mu.Unlock()
			default:
				final = e
			}
		}
		_, _ = io.Copy(io.Discard, stdoutR)
	}()
	stop := make(chan struct{})
	go d.watchIdle(rec, stop)
	opts := download.PutOptions{Home: workerHome, Destination: rec.Destination, Size: rec.Size, MaxBytes: rec.MaxBytes, SHA256: rec.ExpectedSHA256, Overwrite: rec.Overwrite}
	command := append([]string{helperCommand[0]}, download.PutArgs(opts)...)
	execErr := d.Exec(ctx, t.ns, t.worker, "workspace", command, stdin, stdoutW, &stderr)
	close(stop)
	// Unblock a part still being written; the helper is gone.
	stdin.CloseWithError(errHelperEnded)
	stdoutW.Close()
	<-parsed
	d.mu.Lock()
	now := d.now()
	rec.Finished = &now
	switch {
	case final.Event == "done":
		rec.State, rec.Written, rec.SHA256, rec.Path = rec.State.to(uploadDone), final.Bytes, final.SHA256, final.Path
	case rec.aborted != "":
		rec.State, rec.Code, rec.Error = rec.State.to(uploadCancelled), download.CodeCancelled, rec.aborted+"; nothing was kept"
	case final.Event == "error":
		rec.State, rec.Code, rec.Error = rec.State.to(uploadFailed), final.Code, helperMessage(final)
	case strings.Contains(stderr.String(), "unknown command"):
		rec.State, rec.Code, rec.Error = rec.State.to(uploadFailed), CodeTooOld, "image too old: this sandbox's workspace image predates uploads; it gets the upload helper when the project restarts on the updated image"
	case ctx.Err() != nil:
		rec.State, rec.Code, rec.Error = rec.State.to(uploadCancelled), download.CodeCancelled, "the upload timed out; nothing was kept"
	case execErr != nil:
		rec.State, rec.Code, rec.Error = rec.State.to(uploadFailed), "exec", "could not run the upload helper in the sandbox"
	default:
		rec.State, rec.Code, rec.Error = rec.State.to(uploadFailed), "exec", "the sandbox helper ended without a result"
	}
	d.mu.Unlock()
	// Free the sandbox for the next operation before anyone sees the result.
	d.release(t.uid, rec.ID)
	rec.markReady()
	close(rec.ended)
	if err := s.uploadAudit(context.Background(), t.obj, "upload-finished", rec); err != nil {
		slog.Error("upload audit record failed", "workspace_uid", t.uid, "upload", rec.ID, "error", err.Error())
	}
}

// watchIdle aborts an upload whose browser stopped sending parts.
func (d *Downloads) watchIdle(rec *uploadRecord, stop <-chan struct{}) {
	tick := time.NewTicker(uploadIdle / 8)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			d.mu.Lock()
			idle := rec.State == uploadReceiving && d.now().Sub(rec.lastData) > uploadIdle
			d.mu.Unlock()
			if idle && rec.chunk.TryLock() {
				rec.chunk.Unlock()
				d.abortUpload(rec, "no data arrived for 2 minutes (the browser stopped sending)")
			}
		}
	}
}

// abortUpload ends the helper's stdin early. The helper sees fewer bytes than
// announced, removes its temporary file and exits; if it does not, the exec is
// cancelled after a grace period.
func (d *Downloads) abortUpload(rec *uploadRecord, reason string) {
	d.mu.Lock()
	if rec.aborted == "" && rec.Finished == nil {
		rec.aborted = reason
	}
	d.mu.Unlock()
	rec.stdin.CloseWithError(errUploadAborted)
	time.AfterFunc(30*time.Second, rec.cancel)
}

// finishUpload closes stdin after the last byte and waits for the helper's result.
func (d *Downloads) finishUpload(rec *uploadRecord) Upload {
	d.mu.Lock()
	rec.State = rec.State.to(uploadFinishing)
	d.mu.Unlock()
	rec.stdin.Close()
	select {
	case <-rec.ended:
	case <-time.After(uploadFinishWait):
	}
	return d.uploadView(rec)
}

func (d *Downloads) findUpload(w http.ResponseWriter, r *http.Request) *uploadRecord {
	d.mu.Lock()
	rec, ok := d.uploads[r.PathValue("id")]
	d.mu.Unlock()
	if !ok || rec.Workspace != r.PathValue("name") {
		http.Error(w, "no such upload", http.StatusNotFound)
		return nil
	}
	return rec
}

func (s *Server) uploadData(w http.ResponseWriter, r *http.Request) {
	if _, ok := downloadOperator(w, r); !ok {
		return
	}
	d := s.Downloads
	rec := d.findUpload(w, r)
	if rec == nil {
		return
	}
	offset, err := strconv.ParseInt(r.Header.Get("X-Upload-Offset"), 10, 64)
	n := r.ContentLength
	if err != nil || offset < 0 || n <= 0 || n > maxUploadChunk {
		http.Error(w, fmt.Sprintf("send one part of 1 to %d bytes with Content-Length and X-Upload-Offset", maxUploadChunk), http.StatusLengthRequired)
		return
	}
	if !rec.chunk.TryLock() {
		http.Error(w, "another part of this upload is being sent", http.StatusConflict)
		return
	}
	defer rec.chunk.Unlock()
	view := d.uploadView(rec)
	if view.State != uploadReceiving {
		msg := "the upload is " + string(view.State)
		if view.Error != "" {
			msg = view.Error
		}
		http.Error(w, msg, http.StatusConflict)
		return
	}
	if offset != view.Received || offset+n > view.Size {
		http.Error(w, fmt.Sprintf("the next part starts at byte %d of %d", view.Received, view.Size), http.StatusConflict)
		return
	}
	copied, err := io.CopyBuffer(rec.stdin, io.LimitReader(r.Body, n), make([]byte, 64<<10))
	d.mu.Lock()
	rec.Received += copied
	rec.lastData = d.now()
	received := rec.Received
	d.mu.Unlock()
	if err != nil || copied != n {
		if !errors.Is(err, errHelperEnded) && !errors.Is(err, errUploadAborted) {
			d.abortUpload(rec, "the connection from the browser broke during the upload")
		}
		select {
		case <-rec.ended:
		case <-time.After(45 * time.Second):
		}
		view = d.uploadView(rec)
		msg := view.Error
		if msg == "" {
			msg = "the upload was interrupted"
		}
		http.Error(w, msg, uploadErrorStatus(view.Code))
		return
	}
	if received == view.Size {
		view = d.finishUpload(rec)
		if view.State != uploadDone {
			msg := view.Error
			if msg == "" {
				msg = "the sandbox is still finishing the file; check the upload list"
			}
			http.Error(w, msg, uploadErrorStatus(view.Code))
			return
		}
	} else {
		view = d.uploadView(rec)
	}
	writeJSON(w, view)
}

func (s *Server) cancelUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := downloadOperator(w, r); !ok {
		return
	}
	var req struct{}
	if decode(w, r, &req) != nil {
		http.Error(w, "empty JSON object required", http.StatusBadRequest)
		return
	}
	d := s.Downloads
	rec := d.findUpload(w, r)
	if rec == nil {
		return
	}
	d.abortUpload(rec, "cancelled in the console")
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]string{"id": rec.ID})
}

func (s *Server) listUploads(w http.ResponseWriter, r *http.Request) {
	if _, ok := downloadOperator(w, r); !ok {
		return
	}
	d := s.Downloads
	d.mu.Lock()
	out := []Upload{}
	for _, u := range d.uploads {
		if u.Workspace == r.PathValue("name") {
			out = append(out, u.Upload)
		}
	}
	d.mu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	writeJSON(w, out)
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := downloadOperator(w, r); !ok {
		return
	}
	if rec := s.Downloads.findUpload(w, r); rec != nil {
		writeJSON(w, s.Downloads.uploadView(rec))
	}
}

// uploadAudit records the request and the outcome: names, sizes, hashes and
// paths, never file contents.
func (s *Server) uploadAudit(ctx context.Context, obj *api.ProjectWorkspace, action string, rec *uploadRecord) error {
	s.Downloads.mu.Lock()
	u := rec.Upload
	s.Downloads.mu.Unlock()
	data := map[string]string{"name": u.Name, "destination": u.Destination, "size": strconv.FormatInt(u.Size, 10), "overwrite": strconv.FormatBool(u.Overwrite)}
	if u.ExpectedSHA256 != "" {
		data["expectedSha256"] = u.ExpectedSHA256
	}
	msg := fmt.Sprintf("Upload into sandbox requested by %s: %s (%d bytes) to %s", u.RequestedBy, u.Name, u.Size, u.Destination)
	if action == "upload-finished" {
		data["outcome"], data["bytes"] = string(u.State), strconv.FormatInt(u.Written, 10)
		if u.SHA256 != "" {
			data["sha256"] = u.SHA256
		}
		if u.Error != "" {
			data["error"] = u.Error
		}
		msg = fmt.Sprintf("Upload %s: %s to %s", u.State, u.Name, u.Destination)
		if u.State == uploadDone {
			msg += fmt.Sprintf(" (%d bytes, sha256 %s)", u.Written, u.SHA256)
		} else if u.Error != "" {
			msg += " (" + u.Error + ")"
		}
	}
	return s.downloadAudit(ctx, obj, action, rec.ID, u.RequestedBy, withMessage(data, msg))
}
