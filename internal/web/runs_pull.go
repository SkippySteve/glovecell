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
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RunRecordPuller copies run records from read-only publishers into a
// RunRecords directory. Publishers never write to the console and the console
// never reaches into the runtime that publishes them. Each record is checked
// against the publisher's index hash and the run record bounds before it is kept.
type RunRecordPuller struct {
	Dir     string
	Sources []string
	Client  *http.Client
}

const maxRunIndex = 1 << 20

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (p RunRecordPuller) Validate() error {
	if err := (RunRecords{Dir: p.Dir}).Validate(); err != nil {
		return err
	}
	if len(p.Sources) == 0 {
		return errors.New("run record pulling requires at least one source")
	}
	for _, source := range p.Sources {
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("run record source %q must be an http(s) origin", source)
		}
	}
	return nil
}

func (p RunRecordPuller) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", url, limit)
	}
	return body, nil
}

func fileHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Pull fetches every new or changed record from every source once.
func (p RunRecordPuller) Pull(ctx context.Context) error {
	var failures []string
	for _, source := range p.Sources {
		base := strings.TrimSuffix(source, "/")
		raw, err := p.get(ctx, base+"/runs", maxRunIndex)
		var index []struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &index)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		for _, entry := range index {
			if !runID.MatchString(entry.ID) || !sha256Hex.MatchString(entry.SHA256) {
				continue
			}
			path := filepath.Join(p.Dir, entry.ID+".json")
			if fileHash(path) == entry.SHA256 {
				continue
			}
			record, err := p.get(ctx, base+"/runs/"+entry.ID, maxRunRecord)
			if err == nil {
				if sum := sha256.Sum256(record); hex.EncodeToString(sum[:]) != entry.SHA256 {
					err = errors.New("record does not match its index hash")
				} else if _, err = validRunRecord(record, entry.ID); err == nil {
					err = writeRecord(p.Dir, entry.ID, record)
				}
			}
			if err != nil {
				failures = append(failures, fmt.Sprintf("%s %s: %v", base, entry.ID, err))
			}
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// Run pulls at the given interval until ctx ends.
func (p RunRecordPuller) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.Pull(ctx); err != nil && ctx.Err() == nil {
			log.Printf("run record pull incomplete: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// writeRecord writes then renames, so the console never serves a partial record.
func writeRecord(dir, id string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tmp := ".pull-" + id + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, bytes.NewReader(data))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, id+".json")
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}
