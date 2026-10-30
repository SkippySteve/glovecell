package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	apcstore "git.cluster.aimingupward.com/steve/agent-project-controller/internal/store"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/store/storetest"
	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/voice/store"
)

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }

var ctx = context.Background()

func newPostgres(t *testing.T) *store.Postgres {
	t.Helper()
	p := store.NewPostgres(storetest.Pool(t, storetest.Database(t)))
	if err := p.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return p
}

// backends runs one contract against the fake and against PostgreSQL.
func backends(t *testing.T, fn func(t *testing.T, s store.Store)) {
	t.Run("memory", func(t *testing.T) { fn(t, store.NewMemory()) })
	t.Run("postgres", func(t *testing.T) { fn(t, newPostgres(t)) })
}

func prCard(code, repo string, pr int, sha string, exp time.Time) store.Card {
	return store.Card{Kind: store.CardPR, Code: code, Repo: repo, PR: pr, HeadSHA: sha, Title: "t", RoomID: "!a:x", RequestedBy: "gate", State: store.CardOpen, ExpiresAt: exp}
}

func TestContractEventsAndThreads(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		if ok, err := s.ClaimEvent(ctx, "$e1"); !ok || err != nil {
			t.Fatalf("first claim %v %v", ok, err)
		}
		if ok, err := s.ClaimEvent(ctx, "$e1"); ok || err != nil {
			t.Fatalf("second claim %v %v", ok, err)
		}
		if _, err := s.GetThread(ctx, "!r", "$root"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing thread: %v", err)
		}
		for _, target := range []string{"orca", "codex"} {
			if err := s.UpsertThread(ctx, store.Thread{RoomID: "!r", RootEvent: "$root", Target: target}); err != nil {
				t.Fatal(err)
			}
		}
		th, err := s.GetThread(ctx, "!r", "$root")
		if err != nil || th.Target != "codex" {
			t.Fatalf("thread %+v %v", th, err)
		}
	})
}

func TestContractTasks(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		if _, err := s.CreateTask(ctx, store.Task{}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("invalid task: %v", err)
		}
		task, err := s.CreateTask(ctx, store.Task{RoomID: "!r", ThreadRoot: "$t", SourceEvent: "$s", Origin: "voice", Target: "orca", State: store.TaskReceived, Transcript: "fix the build", AudioSHA256: "ab"})
		if err != nil || task.ID == 0 {
			t.Fatalf("create %+v %v", task, err)
		}
		task.State, task.ExternalID = store.TaskRunning, "voice-1"
		if err := s.UpdateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		pending, err := s.PendingTasks(ctx)
		if err != nil || len(pending) != 1 || pending[0].ExternalID != "voice-1" || pending[0].Transcript != "fix the build" {
			t.Fatalf("pending %+v %v", pending, err)
		}
		task.State, task.Result, task.Delivered = store.TaskSucceeded, "done", true
		if err := s.UpdateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if pending, _ := s.PendingTasks(ctx); len(pending) != 0 {
			t.Fatalf("delivered task still pending: %+v", pending)
		}
		got, err := s.GetTask(ctx, task.ID)
		if err != nil || got.Result != "done" || got.State != store.TaskSucceeded {
			t.Fatalf("get %+v %v", got, err)
		}
		second, _ := s.CreateTask(ctx, store.Task{RoomID: "!r", ThreadRoot: "$t", Origin: "text", State: store.TaskReceived})
		if got, err := s.TaskInThread(ctx, "!r", "$t"); err != nil || got.ID != second.ID {
			t.Fatalf("task in thread %+v %v", got, err)
		}
		if _, err := s.TaskInThread(ctx, "!r", "$other"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing thread: %v", err)
		}
		task.ID = 999
		if err := s.UpdateTask(ctx, task); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("update missing: %v", err)
		}
	})
}

func TestContractCardsSingleUse(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		exp := now.Add(24 * time.Hour)
		if _, err := s.CreateCard(ctx, store.Card{Kind: store.CardPR, Code: "12", RoomID: "!a", State: store.CardOpen, ExpiresAt: exp}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("short code accepted: %v", err)
		}
		c, err := s.CreateCard(ctx, prCard("4721", "steve/apc", 459, "abcdef1234", exp))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateCard(ctx, prCard("4721", "steve/apc", 460, "abcdef9999", exp)); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("duplicate open code: %v", err)
		}
		got, err := s.CardByCode(ctx, "4721")
		if err != nil || got.ID != c.ID || got.HeadSHA != "abcdef1234" {
			t.Fatalf("by code %+v %v", got, err)
		}
		if _, err := s.OpenCardFor(ctx, "steve/apc", 459); err != nil {
			t.Fatal(err)
		}
		if open, _ := s.OpenCards(ctx, now); len(open) != 1 {
			t.Fatalf("open %d", len(open))
		}
		// Exactly one concurrent UseCard wins.
		var wg sync.WaitGroup
		wins := make(chan bool, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := s.UseCard(ctx, c.ID, now)
				wins <- err == nil
				if err != nil && !errors.Is(err, store.ErrConflict) {
					t.Errorf("use: %v", err)
				}
			}()
		}
		wg.Wait()
		close(wins)
		n := 0
		for w := range wins {
			if w {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d winners", n)
		}
		if err := s.CloseCard(ctx, c.ID, store.CardOpen, store.CardMerged, ""); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("close from wrong state: %v", err)
		}
		if err := s.CloseCard(ctx, c.ID, store.CardApproved, store.CardMerged, "merged"); err != nil {
			t.Fatal(err)
		}
		used, _ := s.CardByCode(ctx, "4721")
		if used.State != store.CardMerged || used.UsedAt == nil {
			t.Fatalf("used card %+v", used)
		}
		// The code is free again for a new card, which then wins lookups.
		c2, err := s.CreateCard(ctx, prCard("4721", "steve/apc", 461, "1111111111", exp))
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := s.CardByCode(ctx, "4721"); got.ID != c2.ID {
			t.Fatalf("lookup prefers %+v", got)
		}
		codes, err := s.RecentCodes(ctx, now.Add(-time.Hour))
		if err != nil || !codes["4721"] {
			t.Fatalf("recent codes %v %v", codes, err)
		}
		// Expired cards cannot be used.
		c3, _ := s.CreateCard(ctx, prCard("5555", "steve/apc", 7, "2222222222", now.Add(time.Minute)))
		if _, err := s.UseCard(ctx, c3.ID, now.Add(2*time.Minute)); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("expired card used: %v", err)
		}
		if open, _ := s.OpenCards(ctx, now.Add(2*time.Minute)); len(open) != 1 || open[0].ID != c2.ID {
			t.Fatalf("open after expiry %+v", open)
		}
		if err := s.SetCardEvent(ctx, c2.ID, "$card"); err != nil {
			t.Fatal(err)
		}
		approved, _ := s.CardsInState(ctx, store.CardMerged)
		if len(approved) != 1 {
			t.Fatalf("merged cards %d", len(approved))
		}
	})
}

func TestContractListCards(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		exp := time.Now().Add(time.Hour)
		items := []store.ListItem{{CardID: 1, Repo: "steve/a", PR: 1, HeadSHA: "aaaaaaa"}, {CardID: 2, Repo: "steve/b", PR: 2, HeadSHA: "bbbbbbb"}}
		l, err := s.CreateCard(ctx, store.Card{Kind: store.CardList, Code: "8830", Items: items, RoomID: "!a", State: store.CardOpen, ExpiresAt: exp})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetCard(ctx, l.ID)
		if err != nil || len(got.Items) != 2 || got.Items[1].HeadSHA != "bbbbbbb" {
			t.Fatalf("list %+v %v", got, err)
		}
		if open, _ := s.OpenCards(ctx, time.Now()); len(open) != 0 {
			t.Fatal("list card listed as a PR card")
		}
	})
}

func TestContractAuditAndGate(t *testing.T) {
	backends(t, func(t *testing.T, s store.Store) {
		now := time.Now().UTC()
		if _, err := s.AppendAudit(ctx, store.AuditEntry{}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("audit without decision: %v", err)
		}
		for i := 0; i < 3; i++ {
			if _, err := s.AppendAudit(ctx, store.AuditEntry{Time: now.Add(-time.Duration(i) * 40 * time.Minute), Decision: store.DecisionMerged, AudioSHA256: fmt.Sprintf("h%d", i), Sender: "@steve:x", Verified: true}); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.CountDecisions(ctx, store.DecisionMerged, now.Add(-time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("count %d %v", n, err)
		}
		if seen, _ := s.AudioSeen(ctx, "h1"); !seen {
			t.Fatal("audio hash not seen")
		}
		if seen, _ := s.AudioSeen(ctx, ""); seen {
			t.Fatal("empty hash seen")
		}
		list, err := s.ListAudit(ctx, 2)
		if err != nil || len(list) != 2 || list[0].ID < list[1].ID {
			t.Fatalf("audit list %+v %v", list, err)
		}
		g, err := s.GetGateState(ctx)
		if err != nil || g.Paused {
			t.Fatalf("default gate %+v %v", g, err)
		}
		if err := s.SetGateState(ctx, store.GateState{Paused: true, ChangedBy: "@steve:x"}); err != nil {
			t.Fatal(err)
		}
		if g, _ := s.GetGateState(ctx); !g.Paused || g.ChangedBy != "@steve:x" {
			t.Fatalf("gate %+v", g)
		}
	})
}

func TestMemoryDownFailsClosed(t *testing.T) {
	m := store.NewMemory()
	m.Down = true
	if _, err := m.UseCard(ctx, 1, time.Now()); !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := m.GetGateState(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestPostgresUnmigratedFailsClosed(t *testing.T) {
	p := store.NewPostgres(storetest.Pool(t, storetest.Database(t)))
	if _, err := p.ClaimEvent(ctx, "$x"); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("write before migration: %v", err)
	}
	if _, err := p.GetGateState(ctx); !errors.Is(err, store.ErrUnavailable) {
		t.Fatalf("read before migration: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := p.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceMigrationsCoexistWithControllerSchema(t *testing.T) {
	url := storetest.Database(t)
	pool := storetest.Pool(t, url)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	// The controller's schema first, then the voice schema in the same
	// database, then the voice schema down: the controller's stays.
	if err := apcstore.NewPostgres(pool).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
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
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name LIKE 'voice_%'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("down left %d tables (%v)", tables, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('audit_events', 'approvals')").Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("controller tables touched: %d (%v)", tables, err)
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
		if want := fmt.Sprintf("%05d_", i+1); !strings.HasPrefix(e.Name(), want) {
			t.Fatalf("migration %s: want prefix %s", e.Name(), want)
		}
	}
}
