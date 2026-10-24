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

func BenchmarkMetricsStoreRecordParallel(b *testing.B) {
	store, err := openMetricsStore(filepath.Join(b.TempDir(), "metrics.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			b.Errorf("close metrics database: %v", err)
		}
	}()
	b.ReportAllocs()
	b.SetParallelism(16)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		event := requestEvent{Kind: "proxy", Method: "GET", Path: "/__bench/echo", Status: 200}
		for pb.Next() {
			event.Time = time.Now().UTC()
			if err := store.Record(context.Background(), event); err != nil {
				b.Error(err)
				return
			}
		}
	})
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

func TestMetricsStorePersistsMaximumBatchAtomically(t *testing.T) {
	store, err := openMetricsStore(filepath.Join(t.TempDir(), "metrics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close metrics database: %v", err)
		}
	}()

	batch := make([]metricWrite, metricsBatchSize)
	for index := range batch {
		kind := "proxy"
		if index%2 == 0 {
			kind = "honeypot"
		}
		batch[index].event = requestEvent{
			Time:   time.Date(2026, time.October, 24, 12, 0, index, 0, time.UTC),
			Kind:   kind,
			Method: "GET",
			Path:   fmt.Sprintf("/batch/%d", index),
			Status: 200,
		}
	}
	if err := store.persistBatch(context.Background(), batch); err != nil {
		t.Fatalf("persist maximum-size batch: %v", err)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HoneypotHits != metricsBatchSize/2 || snapshot.ProxyHits != metricsBatchSize/2 {
		t.Fatalf("batch totals = honeypot:%d proxy:%d, want %d/%d", snapshot.HoneypotHits, snapshot.ProxyHits, metricsBatchSize/2, metricsBatchSize/2)
	}
	if len(snapshot.RecentEvents) != maxRecentEvents {
		t.Fatalf("recent events = %d, want %d", len(snapshot.RecentEvents), maxRecentEvents)
	}
	if got, want := snapshot.RecentEvents[0].Path, "/batch/255"; got != want {
		t.Fatalf("newest batch event path = %q, want %q", got, want)
	}
	if got, want := snapshot.RecentEvents[len(snapshot.RecentEvents)-1].Path, "/batch/236"; got != want {
		t.Fatalf("oldest batch event path = %q, want %q", got, want)
	}
}

func TestMetricsStoreQueueIsBoundedAndCloseDrainsAcceptedWrites(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metrics.sqlite")
	store, err := openMetricsStoreWithQueueCapacity(databasePath, 1)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.Record(context.Background(), requestEvent{
			Kind: "honeypot", Method: "GET", Path: "/first", Status: 200,
		})
	}()

	deadline := time.Now().Add(time.Second)
	for len(store.queue) != 0 {
		if time.Now().After(deadline) {
			_ = connection.Close()
			_ = store.Close()
			t.Fatal("writer did not take the first queued event")
		}
		time.Sleep(time.Millisecond)
	}
	// Let the writer finish collecting the first event and block waiting for
	// the database connection held by this test.
	time.Sleep(10 * time.Millisecond)

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.Record(context.Background(), requestEvent{
			Kind: "proxy", Method: "GET", Path: "/second", Status: 200,
		})
	}()

	deadline = time.Now().Add(time.Second)
	for len(store.queue) != 1 {
		if time.Now().After(deadline) {
			_ = connection.Close()
			_ = store.Close()
			t.Fatal("second event did not occupy the single queue slot")
		}
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.Record(ctx, requestEvent{
		Kind: "proxy", Method: "GET", Path: "/rejected", Status: 200,
	}); err == nil {
		t.Fatal("record with full queue should stop waiting when its context expires")
	}
	if queued, capacity, writerBusy := store.QueueStats(); queued != 1 || capacity != 1 || !writerBusy {
		t.Fatalf("queue stats = %d/%d, writer busy = %t, want 1/1 and busy", queued, capacity, writerBusy)
	}

	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("first accepted write failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second accepted write failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(context.Background(), requestEvent{
		Kind: "proxy", Method: "GET", Path: "/after-close", Status: 200,
	}); err == nil {
		t.Fatal("record after close should fail")
	}

	store, err = openMetricsStore(databasePath)
	if err != nil {
		t.Fatalf("reopen metrics store: %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("read drained metrics snapshot: %v", err)
	}
	if snapshot.HoneypotHits != 1 || snapshot.ProxyHits != 1 {
		t.Fatalf("drained totals = honeypot:%d proxy:%d, want 1/1", snapshot.HoneypotHits, snapshot.ProxyHits)
	}
	if err := store.Close(); err != nil {
		t.Errorf("close reopened metrics store: %v", err)
	}
}
