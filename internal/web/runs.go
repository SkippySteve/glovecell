package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RunRecords serves factory run records from an operator-configured directory,
// one <run-id>.json per orchestration Run. Records are bounded and must name
// their own run here; the console validates the full schema before rendering.
type RunRecords struct{ Dir string }

var runID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

const (
	maxRunRecord = 2 << 20
	maxRuns      = 200
)

type runHeader struct {
	Version int `json:"version"`
	Run     struct {
		ID          string `json:"id"`
		Objective   string `json:"objective"`
		Host        string `json:"host"`
		CollectedAt string `json:"collectedAt"`
	} `json:"run"`
	Tasks []json.RawMessage `json:"tasks"`
}

func (c RunRecords) Validate() error {
	info, err := os.Stat(c.Dir)
	if !filepath.IsAbs(c.Dir) || err != nil || !info.IsDir() {
		return errors.New("run records require an existing absolute directory")
	}
	return nil
}

// read opens the record inside the directory root, so an id can never reach another path.
func (c RunRecords) read(id string) ([]byte, runHeader, error) {
	var header runHeader
	if !runID.MatchString(id) {
		return nil, header, os.ErrNotExist
	}
	root, err := os.OpenRoot(c.Dir)
	if err != nil {
		return nil, header, err
	}
	defer root.Close()
	f, err := root.Open(id + ".json")
	if err != nil {
		return nil, header, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxRunRecord+1))
	if err != nil {
		return nil, header, err
	}
	header, err = validRunRecord(raw, id)
	return raw, header, err
}

// validRunRecord applies the bounds every served or pulled record must meet.
func validRunRecord(raw []byte, id string) (runHeader, error) {
	var header runHeader
	if len(raw) > maxRunRecord {
		return header, errors.New("run record exceeds the size bound")
	}
	if err := json.Unmarshal(raw, &header); err != nil || header.Version != 1 || header.Run.ID != id {
		return header, errors.New("run record is invalid or names another run")
	}
	return header, nil
}

func (c RunRecords) list(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		http.Error(w, "run records unavailable", 503)
		return
	}
	type summary struct {
		ID          string `json:"id"`
		Objective   string `json:"objective"`
		Host        string `json:"host"`
		CollectedAt string `json:"collectedAt"`
		Tasks       int    `json:"tasks"`
	}
	runs := []summary{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		// Unreadable or invalid records are left out rather than failing the list.
		if _, h, err := c.read(id); err == nil {
			runs = append(runs, summary{h.Run.ID, h.Run.Objective, h.Run.Host, h.Run.CollectedAt, len(h.Tasks)})
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CollectedAt > runs[j].CollectedAt })
	if len(runs) > maxRuns {
		runs = runs[:maxRuns]
	}
	writeJSON(w, runs)
}

func (c RunRecords) get(w http.ResponseWriter, r *http.Request) {
	raw, _, err := c.read(r.PathValue("id"))
	if err != nil {
		http.Error(w, "run record unavailable", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
