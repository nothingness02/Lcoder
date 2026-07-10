package observability

import (
	"database/sql"
	"encoding/json"
	"path/filepath"

	"github.com/lcoder/lcoder/internal/fsutil"
	_ "modernc.org/sqlite"
)

// SQLiteExporter writes observability records to a SQLite database.
type SQLiteExporter struct {
	db *sql.DB
}

// NewSQLiteExporter opens (or creates) a SQLite observability database.
func NewSQLiteExporter(path string) (*SQLiteExporter, error) {
	if err := fsutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLiteExporter{db: db}, nil
}

func initSchema(db *sql.DB) error {
	schema := `
CREATE TABLE IF NOT EXISTS spans (
	trace_id TEXT,
	span_id TEXT PRIMARY KEY,
	parent_id TEXT,
	name TEXT,
	start_time INTEGER,
	end_time INTEGER,
	status TEXT,
	attributes TEXT
);

CREATE TABLE IF NOT EXISTS metrics (
	timestamp INTEGER,
	name TEXT,
	value REAL,
	labels TEXT
);

CREATE INDEX IF NOT EXISTS idx_spans_trace ON spans(trace_id);
CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics(name);
`
	_, err := db.Exec(schema)
	return err
}

// Export writes a record to SQLite.
func (s *SQLiteExporter) Export(record Record) error {
	switch record.Type {
	case "span_start", "span_end":
		if record.Span == nil {
			return nil
		}
		attrs, _ := json.Marshal(record.Span.Attributes)
		_, err := s.db.Exec(
			`INSERT OR REPLACE INTO spans (trace_id, span_id, parent_id, name, start_time, end_time, status, attributes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			record.Span.TraceID,
			record.Span.SpanID,
			record.Span.ParentID,
			record.Span.Name,
			record.Span.StartTime,
			record.Span.EndTime,
			record.Span.Status,
			string(attrs),
		)
		return err
	case "metric":
		if record.Metric == nil {
			return nil
		}
		labels, _ := json.Marshal(record.Metric.Labels)
		_, err := s.db.Exec(
			`INSERT INTO metrics (timestamp, name, value, labels) VALUES (?, ?, ?, ?)`,
			record.Metric.Timestamp,
			record.Metric.Name,
			record.Metric.Value,
			string(labels),
		)
		return err
	}
	return nil
}

// Close closes the database.
func (s *SQLiteExporter) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// QuerySummary returns aggregated summary metrics.
func (s *SQLiteExporter) QuerySummary() (map[string]float64, error) {
	rows, err := s.db.Query(`
		SELECT name, SUM(value) FROM metrics GROUP BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var name string
		var value float64
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	return result, rows.Err()
}
