package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const defaultMetricsDBPath = "honeypot.sqlite"

type metricsStore struct {
	db *sql.DB
}

type metricsSnapshot struct {
	HoneypotHits uint64
	ProxyHits    uint64
	RecentEvents []requestEvent
}

func openMetricsStore(path string) (*metricsStore, error) {
	if path == "" {
		path = defaultMetricsDBPath
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create metrics database directory: %w", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create metrics database file: %w", err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close metrics database file: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open metrics database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &metricsStore{db: db}
	if err := store.configureAndMigrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *metricsStore) configureAndMigrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("configure SQLite busy timeout: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("configure SQLite journal mode: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA synchronous = NORMAL"); err != nil {
		return fmt.Errorf("configure SQLite synchronous mode: %w", err)
	}

	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.db, migrations)
	if err != nil {
		return fmt.Errorf("configure Goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}

func (s *metricsStore) Record(ctx context.Context, event requestEvent) error {
	if event.Kind != "honeypot" && event.Kind != "proxy" {
		return errors.New("invalid metric event kind")
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if event.Status < 100 || event.Status > 599 {
		return errors.New("invalid metric event status")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin metrics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE metrics_totals SET hits = hits + 1 WHERE kind = ?`, event.Kind); err != nil {
		return fmt.Errorf("increment metrics counter: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_events (at_utc, kind, method, path, status) VALUES (?, ?, ?, ?, ?)`,
		event.Time.UTC().Format(time.RFC3339Nano), event.Kind, event.Method, event.Path, event.Status); err != nil {
		return fmt.Errorf("insert metrics event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_events WHERE id NOT IN (SELECT id FROM request_events ORDER BY id DESC LIMIT ?)`, maxRecentEvents); err != nil {
		return fmt.Errorf("prune recent metrics events: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit metrics transaction: %w", err)
	}
	return nil
}

func (s *metricsStore) Snapshot(ctx context.Context) (metricsSnapshot, error) {
	var snapshot metricsSnapshot
	snapshot.RecentEvents = make([]requestEvent, 0, maxRecentEvents)
	rows, err := s.db.QueryContext(ctx, `SELECT kind, hits FROM metrics_totals`)
	if err != nil {
		return metricsSnapshot{}, fmt.Errorf("read metrics totals: %w", err)
	}
	for rows.Next() {
		var kind string
		var hits uint64
		if err := rows.Scan(&kind, &hits); err != nil {
			_ = rows.Close()
			return metricsSnapshot{}, fmt.Errorf("scan metrics totals: %w", err)
		}
		switch kind {
		case "honeypot":
			snapshot.HoneypotHits = hits
		case "proxy":
			snapshot.ProxyHits = hits
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return metricsSnapshot{}, fmt.Errorf("iterate metrics totals: %w", err)
	}
	if err := rows.Close(); err != nil {
		return metricsSnapshot{}, fmt.Errorf("close metrics totals: %w", err)
	}

	eventRows, err := s.db.QueryContext(ctx, `SELECT at_utc, kind, method, path, status FROM request_events ORDER BY id DESC LIMIT ?`, maxRecentEvents)
	if err != nil {
		return metricsSnapshot{}, fmt.Errorf("read recent metrics events: %w", err)
	}
	defer eventRows.Close()
	for eventRows.Next() {
		var event requestEvent
		var timestamp string
		if err := eventRows.Scan(&timestamp, &event.Kind, &event.Method, &event.Path, &event.Status); err != nil {
			return metricsSnapshot{}, fmt.Errorf("scan recent metrics event: %w", err)
		}
		event.Time, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return metricsSnapshot{}, fmt.Errorf("parse recent metrics timestamp: %w", err)
		}
		snapshot.RecentEvents = append(snapshot.RecentEvents, event)
	}
	if err := eventRows.Err(); err != nil {
		return metricsSnapshot{}, fmt.Errorf("iterate recent metrics events: %w", err)
	}
	return snapshot, nil
}

func (s *metricsStore) Close() error {
	return s.db.Close()
}
