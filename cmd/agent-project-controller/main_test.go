package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/network"
)

func TestHandoffIntakeConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "token")
	short := filepath.Join(dir, "short")
	os.WriteFile(good, []byte("intake-token-0123456789abcdef0123456789\n"), 0o600)
	os.WriteFile(short, []byte("short\n"), 0o600)
	for _, c := range []struct {
		url, file, sandbox string
		ok, on             bool
	}{
		{"", "", "", true, false},
		{"", good, "", false, false},
		{"", "", "orca-pilot", false, false},
		{"http://intake:8791", "", "", false, false},
		{"http://intake:8791", filepath.Join(dir, "missing"), "", false, false},
		{"http://intake:8791", short, "", false, false},
		{"http://intake:8791", good, "", true, true},
		{"http://intake:8791", good, "orca-pilot", true, true},
		{"http://intake:8791", good, "../orca", false, false},
	} {
		t.Setenv("HANDOFF_URL", c.url)
		t.Setenv("HANDOFF_TOKEN_FILE", c.file)
		t.Setenv("HANDOFF_SANDBOX", c.sandbox)
		h, err := handoffsFromEnv()
		if (err == nil) != c.ok || (h != nil) != c.on {
			t.Errorf("url=%q file=%q sandbox=%q: handoffs=%v err=%v", c.url, c.file, c.sandbox, h != nil, err)
		}
		if h != nil && h.Sandbox != c.sandbox {
			t.Errorf("sandbox %q, want %q", h.Sandbox, c.sandbox)
		}
		if h != nil && h.Token != "intake-token-0123456789abcdef0123456789" {
			t.Errorf("token not trimmed: %q", h.Token)
		}
	}
}

func TestDownloadConfigurationFailsClosed(t *testing.T) {
	cilium := network.Cilium{}
	native := network.NetworkPolicy{}
	for _, c := range []struct {
		enabled, max, timeout string
		provider              network.Provider
		ok, on                bool
	}{
		{"", "", "", cilium, true, false},
		{"false", "", "", native, true, false},
		{"true", "", "", cilium, true, true},
		{"true", "1073741824", "20m", cilium, true, true},
		{"true", "", "", native, false, false},
		{"", "1073741824", "", cilium, false, false},
		{"true", "0", "", cilium, false, false},
		{"true", "2GiB", "", cilium, false, false},
		{"true", "99999999999999", "", cilium, false, false},
		{"true", "", "10s", cilium, false, false},
		{"true", "", "5h", cilium, false, false},
		{"yes", "", "", cilium, true, false},
	} {
		t.Setenv("DOWNLOADS_ENABLED", c.enabled)
		t.Setenv("DOWNLOAD_MAX_BYTES", c.max)
		t.Setenv("DOWNLOAD_TIMEOUT", c.timeout)
		d, err := downloadsFromEnv(c.provider, nil)
		if (err == nil) != c.ok || (d != nil) != c.on {
			t.Errorf("%+v: on=%v err=%v", c, d != nil, err)
		}
	}
}

func TestLANGrantConfigurationFailsClosed(t *testing.T) {
	cilium := network.Cilium{}
	native := network.NetworkPolicy{}
	for _, c := range []struct {
		allowed, denied string
		provider        network.Provider
		ok, on          bool
	}{
		{"", "", cilium, true, false},
		{"", "", native, true, false},
		{"192.168.110.0/24", "192.168.105.0/24,192.168.110.1", cilium, true, true},
		{"192.168.110.0/24", "", cilium, false, false},
		{"192.168.110.0/24", "192.168.105.0/24", native, false, false},
		{"0.0.0.0/0", "192.168.105.0/24", cilium, false, false},
		{"192.168.110.2", "192.168.105.0/24", cilium, false, false},
	} {
		t.Setenv("LAN_GRANT_ALLOWED_CIDRS", c.allowed)
		t.Setenv("LAN_GRANT_DENIED_CIDRS", c.denied)
		lan, err := lanFromEnv(c.provider)
		if (err == nil) != c.ok || lan.Enabled() != c.on {
			t.Errorf("%q %q %T: enabled=%v err=%v", c.allowed, c.denied, c.provider, lan.Enabled(), err)
		}
	}
}

func TestDatabaseConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	uri := filepath.Join(dir, "uri")
	ca := filepath.Join(dir, "ca.crt")
	os.WriteFile(uri, []byte("postgres://apc:secret@apc-db-rw.control:5432/apc\n"), 0o600)
	os.WriteFile(ca, []byte("not used until connect\n"), 0o600)
	plain := filepath.Join(dir, "plain")
	os.WriteFile(plain, []byte("postgres://apc:secret@apc-db-rw.control:5432/apc?sslmode=disable\n"), 0o600)
	for _, c := range []struct {
		name                 string
		env                  map[string]string
		ok, on               bool
		history, proxyRetain time.Duration
	}{
		{"none", map[string]string{}, true, false, 0, 0},
		{"ca without url", map[string]string{"DATABASE_CA_FILE": ca}, false, false, 0, 0},
		{"missing url file", map[string]string{"DATABASE_URL_FILE": filepath.Join(dir, "missing")}, false, false, 0, 0},
		{"plaintext refused", map[string]string{"DATABASE_URL_FILE": plain, "DATABASE_CA_FILE": ca}, false, false, 0, 0},
		{"bad pool size", map[string]string{"DATABASE_URL_FILE": uri, "DATABASE_MAX_CONNS": "1"}, false, false, 0, 0},
		{"short retention", map[string]string{"DATABASE_URL_FILE": uri, "HISTORY_RETENTION": "1h"}, false, false, 0, 0},
		{"defaults", map[string]string{"DATABASE_URL_FILE": uri}, true, true, 365 * 24 * time.Hour, 30 * 24 * time.Hour},
		{"keep forever", map[string]string{"DATABASE_URL_FILE": uri, "HISTORY_RETENTION": "0", "PROXY_SUMMARY_RETENTION": "168h"}, true, true, 0, 168 * time.Hour},
	} {
		for _, k := range []string{"DATABASE_URL_FILE", "DATABASE_CA_FILE", "DATABASE_MAX_CONNS", "HISTORY_RETENTION", "DECIDED_REQUEST_RETENTION", "PROXY_SUMMARY_RETENTION"} {
			t.Setenv(k, c.env[k])
		}
		db, retention, err := databaseFromEnv(context.Background())
		if (err == nil) != c.ok || (db != nil) != c.on {
			t.Errorf("%s: db=%v err=%v", c.name, db != nil, err)
			continue
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error leaks the password: %v", c.name, err)
		}
		if db != nil {
			if retention.History != c.history || retention.ProxySummaries != c.proxyRetain {
				t.Errorf("%s: retention %+v", c.name, retention)
			}
			db.Close()
		}
	}
}
