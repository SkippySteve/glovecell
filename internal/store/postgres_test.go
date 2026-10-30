package store_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
)

func TestMigrationsUpAndIdempotent(t *testing.T) {
	p, _ := storetest.Unmigrated(t)
	if _, err := p.AppendEvent(ctx, event("u", "k", "a", time.Now())); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("write before migration: %v", err)
	}
	if err := p.Ping(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("ping before migration: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := p.Migrate(ctx); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
	v, err := p.SchemaVersion(ctx)
	if err != nil || v != 1 {
		t.Fatalf("schema version %d %v", v, err)
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsConcurrentUnderAdvisoryLock(t *testing.T) {
	_, url := storetest.Unmigrated(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := store.NewPostgres(storetest.Pool(t, url))
			errs <- p.Migrate(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migrate: %v", err)
		}
	}
	p := store.NewPostgres(storetest.Pool(t, url))
	if err := p.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var applied int
	pool := storetest.Pool(t, url)
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+store.MigrationTable+" WHERE version_id = 1").Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("version 1 applied %d times (%v)", applied, err)
	}
}

func TestMigrationsDownAndUpAgain(t *testing.T) {
	_, url := storetest.Unmigrated(t)
	pool := storetest.Pool(t, url)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, store.Migrations(), goose.WithTableName(store.MigrationTable), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	var tables int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('audit_events','access_requests','approvals','proxy_requests','imports')").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("down left %d tables (%v)", tables, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestMigrationFilesAreVersioned(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		want := fmt.Sprintf("%05d_", i+1)
		if !strings.HasPrefix(e.Name(), want) || !strings.HasSuffix(e.Name(), ".sql") {
			t.Fatalf("migration %s: want prefix %s and .sql", e.Name(), want)
		}
		b, _ := os.ReadFile(filepath.Join("migrations", e.Name()))
		if !strings.Contains(string(b), "-- +goose Up") || !strings.Contains(string(b), "-- +goose Down") {
			t.Fatalf("migration %s lacks goose Up/Down", e.Name())
		}
	}
}

// The database going away refuses every write and read with ErrUnavailable,
// and the store recovers when it returns.
func TestPostgresDownFailsClosed(t *testing.T) {
	storetest.Available(t)
	srv, err := storetest.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	p := store.NewPostgres(storetest.Pool(t, srv.URL))
	if err := p.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r, _, err := p.CreateRequest(ctx, newRequest("uid", "/a"))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	checks := map[string]error{}
	_, _, checks["create"] = p.CreateRequest(short, newRequest("uid", "/b"))
	_, _, checks["decide"] = p.Decide(short, store.Decision{WorkspaceUID: "uid", RequestID: r.ID, Approve: true, Actor: "alice"})
	_, checks["append"] = p.AppendEvent(short, event("uid", "k", "a", time.Now()))
	_, checks["approvals"] = p.ActiveApprovals(short, "uid")
	_, checks["revoke"] = p.RevokeApproval(short, store.Revocation{WorkspaceUID: "uid", ApprovalID: 1, Actor: "alice"})
	_, checks["summaries"] = p.RecordProxySummaries(short, []store.ProxySummary{{WorkspaceUID: "uid", RequestID: "x", Time: time.Now(), Target: target("/a"), Decision: store.ProxyAllow}})
	checks["ping"] = p.Ping(short)
	for name, err := range checks {
		if !errors.Is(err, store.ErrUnavailable) {
			t.Errorf("%s with the database down: %v, want ErrUnavailable", name, err)
		}
	}
}

func TestMemoryDownFailsClosed(t *testing.T) {
	m := store.NewMemory()
	r, _, _ := m.CreateRequest(ctx, newRequest("uid", "/a"))
	m.SetDown(true)
	if _, _, err := m.Decide(ctx, store.Decision{WorkspaceUID: "uid", RequestID: r.ID, Approve: true, Actor: "alice"}); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("decide while down: %v", err)
	}
	m.SetDown(false)
	if got, _ := m.Request(ctx, "uid", r.ID); got.State != store.StatePending {
		t.Fatalf("refused decision changed state: %+v", got)
	}
}

func TestMonitorRetriesMigrationAndReportsHealth(t *testing.T) {
	m := store.NewMemory()
	fail := errors.New("connection refused")
	attempts, readyCalls := 0, 0
	mon := store.NewMonitor(m, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("%w: %v", store.ErrUnavailable, fail)
		}
		return nil
	})
	mon.OnReady = func(context.Context) { readyCalls++ }
	for i := 0; i < 2; i++ {
		if mon.Step(ctx) || mon.Check(nil) == nil || !strings.Contains(mon.Status().Reason, "connection refused") {
			t.Fatalf("step %d reported ready: %+v", i, mon.Status())
		}
	}
	if !mon.Step(ctx) || mon.Check(nil) != nil || readyCalls != 1 {
		t.Fatalf("not ready after migration: %+v (OnReady %d)", mon.Status(), readyCalls)
	}
	m.SetDown(true)
	if mon.Step(ctx) || mon.Check(nil) == nil {
		t.Fatalf("ready while down: %+v", mon.Status())
	}
	m.SetDown(false)
	if !mon.Step(ctx) || readyCalls != 1 || attempts != 3 {
		t.Fatalf("recovery: %+v attempts %d OnReady %d", mon.Status(), attempts, readyCalls)
	}
	var none *store.Monitor
	if none.Check(nil) != nil || none.Status().Configured {
		t.Fatal("nil monitor must report an unconfigured, not failing, database")
	}
}

func TestConfigRequiresVerifyFullTLS(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	caPEM, _, _ := newCA(t, "test CA")
	os.WriteFile(ca, caPEM, 0o600)
	for _, bad := range []string{
		"postgres://u:secret@db:5432/apc?sslmode=disable",
		"postgres://u:secret@db:5432/apc?sslmode=require",
		"postgres://u:secret@db:5432/apc?sslmode=prefer",
		"postgres://u:secret@db:5432/apc?sslmode=verify-ca",
		"postgres://u:secret@db:5432/apc?sslrootcert=/other/ca.crt",
		"host=db user=u password=secret",
		"mysql://u:secret@db/apc",
		"postgres://u:secret@/apc",
	} {
		_, err := store.Config{URL: bad, CAFile: ca}.PoolConfig()
		if err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("error leaks the password: %v", err)
		}
	}
	pc, err := store.Config{URL: "postgres://u:secret@db-rw.apc:5432/apc", CAFile: ca, MaxConns: 3}.PoolConfig()
	if err != nil {
		t.Fatal(err)
	}
	tc := pc.ConnConfig.TLSConfig
	if tc == nil || tc.InsecureSkipVerify || tc.ServerName != "db-rw.apc" || tc.RootCAs == nil || len(pc.ConnConfig.Fallbacks) != 0 || pc.MaxConns != 3 {
		t.Fatalf("TLS not verify-full: %+v fallbacks %d", tc, len(pc.ConnConfig.Fallbacks))
	}
	if pc.ConnConfig.RuntimeParams["application_name"] != "agent-project-controller" {
		t.Fatalf("application_name: %v", pc.ConnConfig.RuntimeParams)
	}
	// External databases with a public certificate use the system roots.
	if pc, err := (store.Config{URL: "postgresql://u:secret@db.example.com/apc?sslmode=verify-full"}).PoolConfig(); err != nil || pc.ConnConfig.TLSConfig.ServerName != "db.example.com" {
		t.Fatalf("system roots: %v", err)
	}
	urlFile := filepath.Join(dir, "uri")
	os.WriteFile(urlFile, []byte("postgres://u:secret@db:5432/apc\n"), 0o600)
	c, err := store.ConfigFromFiles(urlFile, ca)
	if err != nil || c.URL != "postgres://u:secret@db:5432/apc" || c.CAFile != ca {
		t.Fatalf("from files: %+v %v", c, err)
	}
	if _, err := store.ConfigFromFiles(filepath.Join(dir, "missing"), ca); err == nil {
		t.Fatal("missing URL file accepted")
	}
	if _, err := store.ConfigFromFiles(urlFile, filepath.Join(dir, "missing.crt")); err == nil {
		t.Fatal("missing CA file accepted")
	}
}

// A TLS server is reached with verify-full; the wrong CA or host name is
// refused.
func TestPostgresTLSVerifyFull(t *testing.T) {
	storetest.Available(t)
	dir := t.TempDir()
	caPEM, caCert, caKey := newCA(t, "apc test CA")
	certPEM, keyPEM := newServerCert(t, caCert, caKey, "localhost")
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	caFile := write("ca.crt", caPEM)
	srv, err := storetest.Start(map[string]string{"ssl": "on", "ssl_cert_file": write("server.crt", certPEM), "ssl_key_file": write("server.key", keyPEM)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	connect := func(host, ca string) error {
		p, err := store.Open(ctx, store.Config{URL: fmt.Sprintf("postgres://apc:apc-test@%s:%d/postgres", host, srv.Port), CAFile: ca, ConnectTimeout: 5 * time.Second})
		if err != nil {
			return err
		}
		defer p.Close()
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return p.Migrate(c)
	}
	if err := connect("localhost", caFile); err != nil {
		t.Fatalf("verify-full with the right CA: %v", err)
	}
	otherCA, _, _ := newCA(t, "other CA")
	if err := connect("localhost", write("other.crt", otherCA)); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("wrong CA: %v", err)
	}
	if err := connect("127.0.0.1", caFile); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("host name mismatch accepted: %v", err)
	}
}

func newCA(t *testing.T, name string) ([]byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}

func newServerCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string) ([]byte, []byte) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.DNSNames, tmpl.IPAddresses = nil, []net.IP{ip}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}
