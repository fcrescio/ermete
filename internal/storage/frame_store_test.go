package storage

import (
	"testing"
	"time"

	"ermete/internal/observability"

	"github.com/prometheus/client_golang/prometheus"
)

func TestIdempotencyMaxEntriesEviction(t *testing.T) {
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	store, err := NewFrameStore(t.TempDir(), 10*time.Minute, 3, metrics)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, err := store.SaveFrame("f", "", string(rune('a'+i)), "image/png", []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := store.IdempotencySize(); got > 3 {
		t.Fatalf("expected <= 3 idempotency entries, got %d", got)
	}
}

func TestIdempotencyTTLCleanup(t *testing.T) {
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	store, err := NewFrameStore(t.TempDir(), 50*time.Millisecond, 100, metrics)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveFrame("f", "", "idem-key", "image/png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	store.RunCleanup(time.Now().UTC())
	if got := store.IdempotencySize(); got != 0 {
		t.Fatalf("expected expired entries to be removed, got %d", got)
	}
}
