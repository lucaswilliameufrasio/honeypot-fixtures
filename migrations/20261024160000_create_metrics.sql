-- +goose Up
CREATE TABLE metrics_totals (
    kind TEXT PRIMARY KEY CHECK (kind IN ('honeypot', 'proxy')),
    hits INTEGER NOT NULL DEFAULT 0 CHECK (hits >= 0)
);

INSERT INTO metrics_totals (kind, hits) VALUES ('honeypot', 0), ('proxy', 0);

CREATE TABLE request_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    at_utc TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('honeypot', 'proxy')),
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    status INTEGER NOT NULL CHECK (status BETWEEN 100 AND 599)
);

-- +goose Down
DROP TABLE request_events;
DROP TABLE metrics_totals;
