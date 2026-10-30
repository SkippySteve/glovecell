// Package storetest gives tests a real PostgreSQL: the server named by
// APC_TEST_DATABASE_URL (an admin URL; each test gets its own database), or
// an in-process server started with fergusstrange/embedded-postgres, which
// downloads PostgreSQL binaries on first use.
//
// Tests that need it are skipped with -short, with APC_POSTGRES_TEST=skip,
// or when running as root (PostgreSQL refuses to start as root) without
// APC_TEST_DATABASE_URL. A failure to start the embedded server is a test
// failure, so a missing download is never mistaken for a pass.
package storetest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
)

var (
	once     sync.Once
	shared   *Server
	startErr error
	counter  atomic.Int64
)

// Server is one embedded PostgreSQL.
type Server struct {
	db   *embeddedpostgres.EmbeddedPostgres
	URL  string
	Port uint32
	dir  string
	log  *bytes.Buffer
}

// Available skips t when no PostgreSQL may be used and returns an admin URL.
func Available(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: PostgreSQL tests skipped")
	}
	if u := os.Getenv("APC_TEST_DATABASE_URL"); u != "" {
		return u
	}
	if os.Getenv("APC_POSTGRES_TEST") == "skip" {
		t.Skip("APC_POSTGRES_TEST=skip: PostgreSQL tests skipped")
	}
	if os.Geteuid() == 0 {
		t.Skip("embedded PostgreSQL cannot run as root; set APC_TEST_DATABASE_URL to run PostgreSQL tests")
	}
	once.Do(func() { shared, startErr = Start(nil) })
	if startErr != nil {
		t.Fatalf("embedded PostgreSQL did not start: %v (run with -short or APC_POSTGRES_TEST=skip to skip)", startErr)
	}
	return shared.URL
}

func freePort() (uint32, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return uint32(l.Addr().(*net.TCPAddr).Port), nil
}

// Start starts a dedicated embedded server with extra server parameters
// (for example ssl=on). Stop it with Stop.
func Start(params map[string]string) (*Server, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "apc-postgres-")
	if err != nil {
		return nil, err
	}
	log := &bytes.Buffer{}
	cfg := embeddedpostgres.DefaultConfig().Port(port).RuntimePath(dir).Logger(log).StartTimeout(60 * time.Second).
		Username("apc").Password("apc-test").Database("postgres")
	if cache := os.Getenv("APC_POSTGRES_CACHE"); cache != "" {
		cfg = cfg.CachePath(cache)
	}
	if params != nil {
		cfg = cfg.StartParameters(params)
	}
	db := embeddedpostgres.NewDatabase(cfg)
	if err := db.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("%w\n%s", err, log.String())
	}
	return &Server{db: db, Port: port, dir: dir, log: log, URL: fmt.Sprintf("postgres://apc:apc-test@127.0.0.1:%d/postgres?sslmode=disable", port)}, nil
}

// Stop stops the server and removes its files.
func (s *Server) Stop() error {
	err := s.db.Stop()
	os.RemoveAll(s.dir)
	return err
}

// Main runs the tests and stops the shared server afterwards. Call it from
// TestMain in packages that use this helper.
func Main(m *testing.M) int {
	code := m.Run()
	if shared != nil {
		_ = shared.Stop()
	}
	return code
}

// Database creates an empty database for t (dropped at cleanup) and returns
// its URL.
func Database(t testing.TB) string {
	t.Helper()
	admin := Available(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("apc_test_%d_%d", os.Getpid(), counter.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := pgx.Connect(ctx, admin); err == nil {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(ctx)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Pool opens a pool on url, closed at cleanup.
func Pool(t testing.TB, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// New returns a migrated Postgres store on a fresh database.
func New(t testing.TB) *store.Postgres {
	t.Helper()
	p := store.NewPostgres(Pool(t, Database(t)))
	if err := p.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return p
}

// Unmigrated returns a Postgres store on a fresh database without running
// migrations, and its URL.
func Unmigrated(t testing.TB) (*store.Postgres, string) {
	t.Helper()
	u := Database(t)
	return store.NewPostgres(Pool(t, u)), u
}
