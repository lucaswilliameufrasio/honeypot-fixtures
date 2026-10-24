package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMetricsPersistAcrossRestartAndKeepLatestTwentyEvents(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metrics.sqlite")
	store, err := openMetricsStore(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	baseTime := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 25; index++ {
		kind := "proxy"
		if index%2 == 0 {
			kind = "honeypot"
		}
		event := requestEvent{
			Time:   baseTime.Add(time.Duration(index) * time.Second),
			Kind:   kind,
			Method: "GET",
			Path:   fmt.Sprintf("/request/%d", index),
			Status: 200,
		}
		if err := store.Record(context.Background(), event); err != nil {
			t.Fatalf("record event %d: %v", index, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openMetricsStore(databasePath)
	if err != nil {
		t.Fatalf("reopen metrics database: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close metrics database: %v", err)
		}
	}()

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HoneypotHits != 13 || snapshot.ProxyHits != 12 {
		t.Fatalf("persisted totals = honeypot:%d proxy:%d, want honeypot:13 proxy:12", snapshot.HoneypotHits, snapshot.ProxyHits)
	}
	if len(snapshot.RecentEvents) != maxRecentEvents {
		t.Fatalf("recent events = %d, want %d", len(snapshot.RecentEvents), maxRecentEvents)
	}
	if got, want := snapshot.RecentEvents[0].Path, "/request/24"; got != want {
		t.Fatalf("newest event path = %q, want %q", got, want)
	}
	if got, want := snapshot.RecentEvents[len(snapshot.RecentEvents)-1].Path, "/request/5"; got != want {
		t.Fatalf("oldest retained event path = %q, want %q", got, want)
	}
}

func BenchmarkMetricsStoreRecord(b *testing.B) {
	store, err := openMetricsStore(filepath.Join(b.TempDir(), "metrics.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			b.Errorf("close metrics database: %v", err)
		}
	}()
	event := requestEvent{Kind: "proxy", Method: "GET", Path: "/__bench/echo", Status: 200}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		event.Time = time.Now().UTC()
		if err := store.Record(context.Background(), event); err != nil {
			b.Fatal(err)
		}
	}
}

func TestMetricsStoreConcurrentWriters(t *testing.T) {
	store, err := openMetricsStore(filepath.Join(t.TempDir(), "metrics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close metrics database: %v", err)
		}
	}()

	const workers = 8
	const eventsPerWorker = 20
	var workerGroup sync.WaitGroup
	errors := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func(worker int) {
			defer workerGroup.Done()
			for eventIndex := 0; eventIndex < eventsPerWorker; eventIndex++ {
				if err := store.Record(context.Background(), requestEvent{
					Kind:   "proxy",
					Method: "GET",
					Path:   fmt.Sprintf("/worker/%d/%d", worker, eventIndex),
					Status: 200,
				}); err != nil {
					errors <- err
					return
				}
			}
		}(worker)
	}
	workerGroup.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("record concurrent metric: %v", err)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(workers * eventsPerWorker); snapshot.ProxyHits != want {
		t.Fatalf("proxy total = %d, want %d", snapshot.ProxyHits, want)
	}
	if len(snapshot.RecentEvents) != maxRecentEvents {
		t.Fatalf("recent events = %d, want %d", len(snapshot.RecentEvents), maxRecentEvents)
	}
}
