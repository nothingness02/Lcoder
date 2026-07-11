package observability

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/lcoder/lcoder/internal/fsutil"
	"github.com/lcoder/lcoder/internal/paths"
)

// FileExporter writes observability records to a JSONL file.
type FileExporter struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// NewFileExporter creates a file-backed exporter.
func NewFileExporter(path string) (*FileExporter, error) {
	f, err := fsutil.OpenPrivateAppend(path)
	if err != nil {
		return nil, err
	}
	return &FileExporter{path: path, file: f}, nil
}

// Export writes a record as one JSONL line.
func (e *FileExporter) Export(record Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := e.file.Write(data); err != nil {
		return err
	}
	if _, err := e.file.WriteString("\n"); err != nil {
		return err
	}
	return nil
}

// Close flushes and closes the underlying file.
func (e *FileExporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.file == nil {
		return nil
	}
	return e.file.Close()
}

// MemoryExporter keeps all records in memory for testing.
type MemoryExporter struct {
	mu      sync.Mutex
	Records []Record
}

// NewMemoryExporter creates an in-memory exporter.
func NewMemoryExporter() *MemoryExporter {
	return &MemoryExporter{Records: make([]Record, 0)}
}

// Export appends a record to memory.
func (e *MemoryExporter) Export(record Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Records = append(e.Records, record)
	return nil
}

// Close is a no-op for MemoryExporter.
func (e *MemoryExporter) Close() error { return nil }

// SummarizeFile loads an observability JSONL file and returns aggregated stats.
func SummarizeFile(path string) (SessionStats, error) {
	records, err := LoadFile(path)
	if err != nil {
		return SessionStats{}, err
	}
	stats := ComputeStats(records)
	return stats, nil
}

// ExportFile loads observability records and writes them via the provided exporter.
func ExportFile(path string, exporter Exporter, output string) error {
	records, err := LoadFile(path)
	if err != nil {
		return err
	}
	defer exporter.Close()

	for _, r := range records {
		if err := exporter.Export(r); err != nil {
			return err
		}
	}

	switch ex := exporter.(type) {
	case *HTMLExporter:
		return ex.Save(output + ".html")
	case *MarkdownExporter:
		return ex.Save(output + ".md")
	}
	return nil
}

// DefaultPath returns the default observability log path for a session.
func DefaultPath(sessionID string) string {
	return paths.LCoderHome("observability", "sessions", sessionID+".jsonl")
}

// LoadFile reads all records from an observability JSONL file.
func LoadFile(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("invalid record: %w", err)
		}
		record, err := unmarshalRecord(raw)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, scanner.Err()
}

func unmarshalRecord(raw map[string]any) (Record, error) {
	typ, ok := raw["type"].(string)
	if !ok {
		return Record{}, fmt.Errorf("record missing type")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return Record{}, err
	}
	switch typ {
	case "span_start", "span_end":
		var span Span
		if err := json.Unmarshal(data, &span); err != nil {
			return Record{}, err
		}
		return Record{Type: typ, Span: &span}, nil
	case "metric":
		var metric Metric
		if err := json.Unmarshal(data, &metric); err != nil {
			return Record{}, err
		}
		return Record{Type: typ, Metric: &metric}, nil
	default:
		return Record{Type: typ}, nil
	}
}
