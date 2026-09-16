package whatsmeow_service

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestSharedAuthStorePostgres(t *testing.T) {
	dsn := os.Getenv("EVOGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("EVOGO_TEST_POSTGRES_DSN not set")
	}
	monitor, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()

	shared := &sharedAuthStore{}
	defer func() {
		if shared.container != nil {
			_ = shared.container.Close()
		}
	}()
	var opens atomic.Int32
	const reconnects = 100
	var wg sync.WaitGroup
	for i := 0; i < reconnects; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			container, err := shared.getOrCreate(func() (*sqlstore.Container, error) {
				opens.Add(1)
				return sqlstore.New(context.Background(), "postgres", dsn, nil)
			})
			if err != nil {
				t.Errorf("open auth store: %v", err)
				return
			}
			if container != shared.container {
				t.Error("received a different container")
			}
			if shared.newDevice() == nil {
				t.Error("new device is nil")
			}
		}()
	}
	wg.Wait()
	if opens.Load() != 1 {
		t.Fatalf("expected one SQL pool for %d starts, got %d", reconnects, opens.Load())
	}
	var connections int
	if err := monitor.QueryRowContext(context.Background(),
		"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND usename = current_user",
	).Scan(&connections); err != nil {
		t.Fatal(err)
	}
	// One connection is the monitor itself; the auth store should need at most
	// one more after initialization. This catches a new pool per reconnect.
	if connections > 2 {
		t.Fatalf("too many PostgreSQL connections after %d starts: %d", reconnects, connections)
	}
}
