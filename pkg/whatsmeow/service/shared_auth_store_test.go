package whatsmeow_service

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestSharedAuthStoreConcurrentInitialization(t *testing.T) {
	shared := &sharedAuthStore{}
	var calls atomic.Int32
	const workers = 64
	results := make(chan *sqlstore.Container, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			container, err := shared.getOrCreate(func() (*sqlstore.Container, error) {
				calls.Add(1)
				return &sqlstore.Container{}, nil
			})
			if err != nil {
				t.Errorf("getOrCreate: %v", err)
				return
			}
			results <- container
		}()
	}
	wg.Wait()
	close(results)
	if calls.Load() != 1 {
		t.Fatalf("expected one database pool, got %d", calls.Load())
	}
	for container := range results {
		if container != shared.container {
			t.Fatal("workers received different containers")
		}
	}
}

func TestSharedAuthStoreRetriesFailedInitialization(t *testing.T) {
	shared := &sharedAuthStore{}
	wantErr := errors.New("database unavailable")
	if _, err := shared.getOrCreate(func() (*sqlstore.Container, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("expected database error, got %v", err)
	}
	want := &sqlstore.Container{}
	got, err := shared.getOrCreate(func() (*sqlstore.Container, error) {
		return want, nil
	})
	if err != nil || got != want {
		t.Fatalf("retry failed: container=%p error=%v", got, err)
	}
}
