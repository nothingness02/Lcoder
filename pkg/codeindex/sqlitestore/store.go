// Package sqlitestore persists a code graph in SQLite and performs incremental
// re-indexing based on file modification time and size. It implements the
// codeindex.Indexer interface.
package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lcoder/lcoder/internal/paths"
	"github.com/lcoder/lcoder/pkg/codeindex"
	"github.com/lcoder/lcoder/pkg/codeindex/multiparser"

	_ "modernc.org/sqlite"
)

// DefaultPath returns the default SQLite index path for a project directory.
func DefaultPath(cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	hash := fmt.Sprintf("%x", sum)[:16]
	return paths.LCoderHome("index", hash, "index.db")
}

// Indexer stores a repository code graph in SQLite.
type Indexer struct {
	mu       sync.Mutex
	dbPath   string
	db       *sql.DB
	parser   *multiparser.Indexer
	exclude  []string
	snapshot *codeindex.Snapshot
}

// NewIndexer creates a SQLite-backed indexer. dbPath may be empty to use the
// default project path derived from cwd.
func NewIndexer(languages, exclude []string, dbPath string) (*Indexer, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("dbPath required")
	}
	idx := &Indexer{
		dbPath:   dbPath,
		parser:   multiparser.NewIndexer(languages, exclude),
		exclude:  exclude,
		snapshot: codeindex.NewSnapshot(),
	}
	if err := idx.open(); err != nil {
		return nil, err
	}
	return idx, nil
}

func (idx *Indexer) open() error {
	if err := os.MkdirAll(filepath.Dir(idx.dbPath), 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", idx.dbPath+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return err
	}
	idx.db = db
	return idx.migrate()
}

func (idx *Indexer) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS files (
    path TEXT PRIMARY KEY,
    mod_time INTEGER NOT NULL,
    size INTEGER NOT NULL,
    indexed_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nodes (
    id INTEGER PRIMARY KEY,
    node_id TEXT UNIQUE NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    qualified_name TEXT NOT NULL,
    file_path TEXT NOT NULL,
    language TEXT NOT NULL,
    start_line INTEGER,
    end_line INTEGER,
    start_column INTEGER,
    end_column INTEGER,
    docstring TEXT,
    signature TEXT,
    visibility TEXT,
    is_exported INTEGER,
    is_async INTEGER,
    is_static INTEGER,
    is_abstract INTEGER,
    decorators_json TEXT,
    type_parameters_json TEXT,
    return_type TEXT,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nodes_file ON nodes(file_path);
CREATE INDEX IF NOT EXISTS idx_nodes_kind ON nodes(kind);
CREATE INDEX IF NOT EXISTS idx_nodes_name ON nodes(name);

CREATE TABLE IF NOT EXISTS edges (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    target TEXT NOT NULL,
    kind TEXT NOT NULL,
    line INTEGER,
    column INTEGER,
    provenance TEXT,
    metadata_json TEXT,
    UNIQUE(source, target, kind, line, column)
);
CREATE INDEX IF NOT EXISTS idx_edges_source ON edges(source);
CREATE INDEX IF NOT EXISTS idx_edges_target ON edges(target);

CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(
    name, qualified_name, signature, docstring,
    content='nodes', content_rowid='id'
);

CREATE TABLE IF NOT EXISTS name_segment_vocab (
    segment TEXT PRIMARY KEY,
    count INTEGER NOT NULL DEFAULT 0
);

CREATE TRIGGER IF NOT EXISTS nodes_ai AFTER INSERT ON nodes BEGIN
    INSERT INTO nodes_fts(rowid, name, qualified_name, signature, docstring)
    VALUES (new.id, new.name, new.qualified_name, new.signature, new.docstring);
END;

CREATE TRIGGER IF NOT EXISTS nodes_ad AFTER DELETE ON nodes BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, signature, docstring)
    VALUES ('delete', old.id, old.name, old.qualified_name, old.signature, old.docstring);
END;

CREATE TRIGGER IF NOT EXISTS nodes_au AFTER UPDATE ON nodes BEGIN
    INSERT INTO nodes_fts(nodes_fts, rowid, name, qualified_name, signature, docstring)
    VALUES ('delete', old.id, old.name, old.qualified_name, old.signature, old.docstring);
    INSERT INTO nodes_fts(rowid, name, qualified_name, signature, docstring)
    VALUES (new.id, new.name, new.qualified_name, new.signature, new.docstring);
END;

CREATE TABLE IF NOT EXISTS unresolved_refs (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    target TEXT NOT NULL,
    kind TEXT NOT NULL,
    line INTEGER,
    column INTEGER,
    context TEXT,
    resolved INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_unresolved_target ON unresolved_refs(target);
CREATE INDEX IF NOT EXISTS idx_unresolved_source ON unresolved_refs(source);
`
	_, err := idx.db.Exec(schema)
	return err
}

type fileRecord struct {
	modTime int64
	size    int64
}

// Update walks root and incrementally re-indexes changed or new files.
func (idx *Indexer) Update(ctx context.Context, root string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		if err := idx.open(); err != nil {
			return err
		}
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Load known file metadata once so unchanged files do not require a
	// per-file SELECT during the walk.
	known := make(map[string]fileRecord)
	rows, err := tx.QueryContext(ctx, "SELECT path, mod_time, size FROM files")
	if err != nil {
		return err
	}
	for rows.Next() {
		var p string
		var modTime, size int64
		if err := rows.Scan(&p, &modTime, &size); err != nil {
			rows.Close()
			return err
		}
		known[p] = fileRecord{modTime: modTime, size: size}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	fullRebuild := len(known) == 0
	snapshot := codeindex.NewSnapshot()
	changed := make(map[string]struct{})
	filesDeleted := false

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if idx.isExcluded(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if idx.isExcluded(rel) {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !idx.parser.HandlesExtension(ext) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		if rec, ok := known[rel]; ok {
			if rec.modTime == info.ModTime().UnixNano() && rec.size == info.Size() {
				delete(known, rel)
				return nil
			}
		}

		ids, _ := idx.nodeIDsForFile(ctx, tx, rel)
		for _, id := range ids {
			changed[id] = struct{}{}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE file_path = ?", rel); err != nil {
			return err
		}
		delete(known, rel)
		snapshot.Nodes = nil
		snapshot.Edges = nil
		snapshot.Files = make(map[string]codeindex.FileMeta)
		if err := idx.parser.ParseFile(snapshot, rel, path); err != nil {
			// Record failure but keep going.
			if _, err := tx.ExecContext(ctx,
				"INSERT OR REPLACE INTO files(path, mod_time, size, indexed_at) VALUES(?, ?, ?, ?)",
				rel, info.ModTime().UnixNano(), info.Size(), time.Now().Unix()); err != nil {
				return err
			}
			return nil
		}
		for _, n := range snapshot.Nodes {
			changed[n.ID] = struct{}{}
			if err := insertNode(ctx, tx, n); err != nil {
				return err
			}
		}
		for _, e := range snapshot.Edges {
			if err := insertEdge(ctx, tx, e); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO files(path, mod_time, size, indexed_at) VALUES(?, ?, ?, ?)",
			rel, info.ModTime().UnixNano(), info.Size(), time.Now().Unix()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Remove files deleted since last update.
	for p := range known {
		ids, _ := idx.nodeIDsForFile(ctx, tx, p)
		for _, id := range ids {
			changed[id] = struct{}{}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE file_path = ?", p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM files WHERE path = ?", p); err != nil {
			return err
		}
		filesDeleted = true
	}

	if fullRebuild {
		if err := idx.resolveEdges(ctx, tx, nil, false); err != nil {
			return err
		}
	} else if len(changed) > 0 {
		if err := idx.resolveEdges(ctx, tx, mapKeys(changed), filesDeleted); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateFiles re-indexes only the provided relative paths. It is intended for
// file watchers that already know which files changed, avoiding a full tree walk.
func (idx *Indexer) UpdateFiles(ctx context.Context, root string, paths []string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		if err := idx.open(); err != nil {
			return err
		}
	}

	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	snapshot := codeindex.NewSnapshot()
	changed := make(map[string]struct{})
	filesDeleted := false
	now := time.Now().Unix()

	for _, rel := range paths {
		rel = filepath.ToSlash(rel)
		if idx.isExcluded(rel) {
			continue
		}
		path := filepath.Join(root, rel)
		info, err := os.Stat(path)
		if err != nil {
			// File was deleted: remove its nodes and record.
			ids, _ := idx.nodeIDsForFile(ctx, tx, rel)
			for _, id := range ids {
				changed[id] = struct{}{}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE file_path = ?", rel); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM files WHERE path = ?", rel); err != nil {
				return err
			}
			filesDeleted = true
			continue
		}
		ext := strings.ToLower(filepath.Ext(path))
		if !idx.parser.HandlesExtension(ext) {
			continue
		}

		ids, _ := idx.nodeIDsForFile(ctx, tx, rel)
		for _, id := range ids {
			changed[id] = struct{}{}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM nodes WHERE file_path = ?", rel); err != nil {
			return err
		}
		snapshot.Nodes = nil
		snapshot.Edges = nil
		snapshot.Files = make(map[string]codeindex.FileMeta)
		if err := idx.parser.ParseFile(snapshot, rel, path); err != nil {
			if _, err := tx.ExecContext(ctx,
				"INSERT OR REPLACE INTO files(path, mod_time, size, indexed_at) VALUES(?, ?, ?, ?)",
				rel, info.ModTime().UnixNano(), info.Size(), now); err != nil {
				return err
			}
			continue
		}
		for _, n := range snapshot.Nodes {
			changed[n.ID] = struct{}{}
			if err := insertNode(ctx, tx, n); err != nil {
				return err
			}
		}
		for _, e := range snapshot.Edges {
			if err := insertEdge(ctx, tx, e); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO files(path, mod_time, size, indexed_at) VALUES(?, ?, ?, ?)",
			rel, info.ModTime().UnixNano(), info.Size(), now); err != nil {
			return err
		}
	}

	if len(changed) > 0 {
		if err := idx.resolveEdges(ctx, tx, mapKeys(changed), filesDeleted); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Search runs a hybrid query over the SQLite graph: exact symbol matches score
// highest, FTS5 matches are next, and substring LIKE matches are the fallback.
func (idx *Indexer) Search(ctx context.Context, q codeindex.Query) ([]codeindex.Result, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return nil, fmt.Errorf("store not open")
	}
	max := q.MaxResults
	if max <= 0 {
		max = 10
	}

	candidates := make(map[string]codeindex.Node)
	scores := make(map[string]float64)

	// Exact symbol matches.
	for _, sym := range q.Symbols {
		s := strings.TrimSpace(sym)
		if s == "" {
			continue
		}
		rows, err := idx.db.QueryContext(ctx, nodeSelectSQL+" WHERE (node_id = ? OR name = ? OR qualified_name = ?) AND n.kind != 'file'", s, s, s)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			candidates[n.ID] = n
			scores[n.ID] += 10.0
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// FTS5 keyword search.
	if len(q.Keywords) > 0 {
		match := ftsMatchExpr(q.Keywords)
		rows, err := idx.db.QueryContext(ctx, nodeSelectSQL+
			" JOIN nodes_fts f ON n.id = f.rowid WHERE nodes_fts MATCH ? AND n.kind != 'file'", match)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			candidates[n.ID] = n
			scores[n.ID] += 5.0
			for _, kw := range q.Keywords {
				scores[n.ID] += keywordBoost(n, kw)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Substring fallback for keywords not handled well by FTS (e.g. partial words).
	if len(q.Keywords) > 0 {
		var filters []string
		var args []any
		for _, kw := range q.Keywords {
			filters = append(filters, "name LIKE ? OR qualified_name LIKE ? OR signature LIKE ? OR docstring LIKE ?")
			like := "%" + escapeLike(kw) + "%"
			args = append(args, like, like, like, like)
		}
		where := "(" + strings.Join(filters, ") OR (") + ") AND n.kind != 'file'"
		if len(q.Kinds) > 0 {
			where += kindInClause(q.Kinds)
			for _, k := range q.Kinds {
				args = append(args, string(k))
			}
		}
		rows, err := idx.db.QueryContext(ctx, nodeSelectSQL+" WHERE "+where, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if _, ok := candidates[n.ID]; !ok {
				candidates[n.ID] = n
				scores[n.ID] += 1.0
				for _, kw := range q.Keywords {
					scores[n.ID] += keywordBoost(n, kw)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if len(q.Kinds) > 0 {
		allowed := make(map[string]bool)
		for _, k := range q.Kinds {
			allowed[string(k)] = true
		}
		for id, n := range candidates {
			if !allowed[string(n.Kind)] {
				delete(candidates, id)
				delete(scores, id)
			}
		}
	}

	results := make([]codeindex.Result, 0, len(candidates))
	for id, n := range candidates {
		results = append(results, codeindex.Result{
			Node:      n,
			Relevance: scores[id],
			Stub:      formatStub(n),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Relevance > results[j].Relevance
	})
	if len(results) > max {
		results = results[:max]
	}
	return results, nil
}

// NodeByID returns a node by its graph ID, or false if it is not indexed.
func (idx *Indexer) NodeByID(ctx context.Context, id string) (codeindex.Node, bool, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return codeindex.Node{}, false, fmt.Errorf("store not open")
	}
	row := idx.db.QueryRowContext(ctx, nodeSelectSQL+" WHERE node_id = ?", id)
	n, err := scanNode(row)
	if err == sql.ErrNoRows {
		return codeindex.Node{}, false, nil
	}
	if err != nil {
		return codeindex.Node{}, false, err
	}
	return n, true, nil
}

// Neighbors returns edges adjacent to a node. direction is "in", "out", or "both".
func (idx *Indexer) Neighbors(ctx context.Context, nodeID string, kinds []codeindex.EdgeKind, direction string) ([]codeindex.Edge, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return nil, fmt.Errorf("store not open")
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	kindStrs := make([]string, len(kinds))
	for i, k := range kinds {
		kindStrs[i] = string(k)
	}
	inKinds := make([]any, len(kinds))
	copyKinds := make([]any, len(kinds))
	for i, k := range kindStrs {
		inKinds[i] = k
		copyKinds[i] = k
	}

	var queries []string
	var args []any
	switch direction {
	case "in":
		queries = append(queries, "SELECT source, target, kind, line, column, provenance, metadata_json FROM edges WHERE target = ? AND kind IN ("+placeholders(len(kinds))+")")
		args = append(args, nodeID)
		args = append(args, inKinds...)
	case "out":
		queries = append(queries, "SELECT source, target, kind, line, column, provenance, metadata_json FROM edges WHERE source = ? AND kind IN ("+placeholders(len(kinds))+")")
		args = append(args, nodeID)
		args = append(args, copyKinds...)
	default:
		queries = append(queries, "SELECT source, target, kind, line, column, provenance, metadata_json FROM edges WHERE source = ? AND kind IN ("+placeholders(len(kinds))+")")
		args = append(args, nodeID)
		args = append(args, inKinds...)
		queries = append(queries, "SELECT source, target, kind, line, column, provenance, metadata_json FROM edges WHERE target = ? AND kind IN ("+placeholders(len(kinds))+")")
		args = append(args, nodeID)
		args = append(args, copyKinds...)
	}

	var edges []codeindex.Edge
	for i, q := range queries {
		rows, err := idx.db.QueryContext(ctx, q, argsForQuery(args, i, len(kinds)+1)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var e codeindex.Edge
			var metadata sql.NullString
			var provenance sql.NullString
			var line, column sql.NullInt64
			if err := rows.Scan(&e.Source, &e.Target, &e.Kind, &line, &column, &provenance, &metadata); err != nil {
				rows.Close()
				return nil, err
			}
			e.Line = int(line.Int64)
			e.Column = int(column.Int64)
			e.Provenance = provenance.String
			if metadata.Valid && metadata.String != "" {
				_ = json.Unmarshal([]byte(metadata.String), &e.Metadata)
			}
			edges = append(edges, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return edges, nil
}

func keywordBoost(n codeindex.Node, kw string) float64 {
	if strings.EqualFold(n.Name, kw) || strings.EqualFold(n.QualifiedName, kw) {
		return 3.0
	}
	if strings.HasSuffix(strings.ToLower(n.Name), "."+strings.ToLower(kw)) {
		return 2.0
	}
	return 0.0
}

func (idx *Indexer) resolveEdges(ctx context.Context, tx *sql.Tx, changed []string, filesDeleted bool) error {
	if changed != nil && len(changed) == 0 && !filesDeleted {
		return nil
	}

	// Full rebuild: use broad scans. This is used on first index or when the
	// caller explicitly passes nil for changed.
	if changed == nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM edges WHERE kind != 'imports' AND source NOT IN (SELECT node_id FROM nodes)"); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT e.id, e.source, e.target, e.kind, e.line, e.column
			FROM edges e
			LEFT JOIN nodes n ON e.target = n.node_id
			WHERE n.node_id IS NULL AND e.kind IN ('calls', 'references')`)
		if err != nil {
			return err
		}
		return idx.resolveRows(ctx, tx, rows)
	}

	// Incremental: only reconsider edges originating from changed sources.
	args := make([]any, len(changed))
	for i, c := range changed {
		args[i] = c
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM edges WHERE source IN ("+placeholders(len(changed))+")", args...); err != nil {
		return err
	}

	if filesDeleted {
		// A file was removed, so stale incoming edges may exist anywhere.
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM edges WHERE kind != 'imports' AND (source NOT IN (SELECT node_id FROM nodes) OR target NOT IN (SELECT node_id FROM nodes))"); err != nil {
			return err
		}
	}

	query := "SELECT e.id, e.source, e.target, e.kind, e.line, e.column FROM edges e LEFT JOIN nodes n ON e.target = n.node_id WHERE n.node_id IS NULL AND e.kind IN ('calls','references') AND e.source IN (" + placeholders(len(changed)) + ")"
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	return idx.resolveRows(ctx, tx, rows)
}

func (idx *Indexer) resolveRows(ctx context.Context, tx *sql.Tx, rows *sql.Rows) error {
	defer rows.Close()
	var unresolved []struct {
		id     int64
		source string
		target string
		kind   string
		line   int64
		column int64
	}
	for rows.Next() {
		var id int64
		var source, target, kind string
		var line, column sql.NullInt64
		if err := rows.Scan(&id, &source, &target, &kind, &line, &column); err != nil {
			return err
		}
		unresolved = append(unresolved, struct {
			id     int64
			source string
			target string
			kind   string
			line   int64
			column int64
		}{
			id:     id,
			source: source,
			target: target,
			kind:   kind,
			line:   line.Int64,
			column: column.Int64,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, u := range unresolved {
		resolved, ok, err := idx.resolveTarget(ctx, tx, u.target)
		if err != nil {
			return err
		}
		if ok {
			if _, err := tx.ExecContext(ctx, "UPDATE edges SET target = ? WHERE id = ?", resolved, u.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM unresolved_refs WHERE source = ? AND target = ? AND kind = ?",
				u.source, u.target, u.kind); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO unresolved_refs(source, target, kind, line, column, context)
			VALUES (?, ?, ?, ?, ?, ?)`,
			u.source, u.target, u.kind, u.line, u.column, ""); err != nil {
			return err
		}
	}
	return nil
}

func (idx *Indexer) resolveTarget(ctx context.Context, tx *sql.Tx, target string) (string, bool, error) {
	var nodeID string
	err := tx.QueryRowContext(ctx,
		"SELECT node_id FROM nodes WHERE node_id = ? OR name = ? OR qualified_name = ? LIMIT 1",
		target, target, target).Scan(&nodeID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return nodeID, true, nil
}

func (idx *Indexer) nodeIDsForFile(ctx context.Context, tx *sql.Tx, filePath string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT node_id FROM nodes WHERE file_path = ?", filePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func mapKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func placeholders(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = "?"
	}
	return strings.Join(p, ",")
}

func argsForQuery(args []any, queryIdx, argCount int) []any {
	start := queryIdx * argCount
	if start+argCount > len(args) {
		return args[start:]
	}
	return args[start : start+argCount]
}

const nodeSelectSQL = `
SELECT n.node_id, n.kind, n.name, n.qualified_name, n.file_path, n.language,
       n.start_line, n.end_line, n.start_column, n.end_column, n.docstring,
       n.signature, n.visibility, n.is_exported, n.is_async, n.is_static,
       n.is_abstract, n.decorators_json, n.type_parameters_json, n.return_type, n.updated_at
FROM nodes n`

func formatStub(n codeindex.Node) string {
	if n.Signature == "" {
		return fmt.Sprintf("// %s:%d\n%s %s", n.FilePath, n.StartLine, n.Kind, n.Name)
	}
	return fmt.Sprintf("// %s:%d\n%s", n.FilePath, n.StartLine, n.Signature)
}

func ftsMatchExpr(keywords []string) string {
	var parts []string
	for _, kw := range keywords {
		parts = append(parts, quoteFTS(kw))
	}
	return strings.Join(parts, " OR ")
}

func quoteFTS(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

func kindInClause(kinds []codeindex.NodeKind) string {
	placeholders := make([]string, len(kinds))
	for i := range kinds {
		placeholders[i] = "?"
	}
	return " AND kind IN (" + strings.Join(placeholders, ",") + ")"
}

func scanNode(s scanner) (codeindex.Node, error) {
	var n codeindex.Node
	var decorators, typeParams sql.NullString
	var docstring, signature, visibility, returnType sql.NullString
	var exported, async, static, abstract sql.NullInt64
	var updated int64
	err := s.Scan(
		&n.ID, &n.Kind, &n.Name, &n.QualifiedName, &n.FilePath, &n.Language,
		&n.StartLine, &n.EndLine, &n.StartColumn, &n.EndColumn, &docstring,
		&signature, &visibility, &exported, &async, &static,
		&abstract, &decorators, &typeParams, &returnType, &updated,
	)
	if err != nil {
		return n, err
	}
	n.Docstring = docstring.String
	n.Signature = signature.String
	n.Visibility = visibility.String
	n.IsExported = exported.Int64 != 0
	n.IsAsync = async.Int64 != 0
	n.IsStatic = static.Int64 != 0
	n.IsAbstract = abstract.Int64 != 0
	n.ReturnType = returnType.String
	n.UpdatedAt = updated
	if decorators.Valid && decorators.String != "" {
		_ = json.Unmarshal([]byte(decorators.String), &n.Decorators)
	}
	if typeParams.Valid && typeParams.String != "" {
		_ = json.Unmarshal([]byte(typeParams.String), &n.TypeParameters)
	}
	return n, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// Stats returns aggregate counts for the indexed graph.
func (idx *Indexer) Stats(ctx context.Context) (files, nodes, edges, unresolved int64, err error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return 0, 0, 0, 0, fmt.Errorf("store not open")
	}
	_ = idx.db.QueryRowContext(ctx, "SELECT count(*) FROM files").Scan(&files)
	_ = idx.db.QueryRowContext(ctx, "SELECT count(*) FROM nodes").Scan(&nodes)
	_ = idx.db.QueryRowContext(ctx, "SELECT count(*) FROM edges").Scan(&edges)
	_ = idx.db.QueryRowContext(ctx, "SELECT count(*) FROM unresolved_refs").Scan(&unresolved)
	return
}

// AllNodes returns every indexed node (excluding file nodes).
func (idx *Indexer) AllNodes(ctx context.Context) ([]codeindex.Node, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return nil, fmt.Errorf("store not open")
	}
	rows, err := idx.db.QueryContext(ctx, nodeSelectSQL+" WHERE n.kind != 'file'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []codeindex.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// Clear removes all persisted nodes, edges, and file records.
func (idx *Indexer) Clear() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return nil
	}
	_, err := idx.db.Exec(`
		DELETE FROM edges;
		DELETE FROM nodes;
		DELETE FROM files;
		DELETE FROM nodes_fts;
		DELETE FROM name_segment_vocab;
	`)
	idx.snapshot = codeindex.NewSnapshot()
	return err
}

// Close closes the underlying database.
func (idx *Indexer) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.db == nil {
		return nil
	}
	return idx.db.Close()
}

func (idx *Indexer) isExcluded(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range idx.exclude {
		p = filepath.ToSlash(p)
		if p == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(p, "/"); ok {
			if rel == dir || strings.HasPrefix(rel, dir+"/") {
				return true
			}
			continue
		}
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(p, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}

func insertNode(ctx context.Context, tx *sql.Tx, n codeindex.Node) error {
	decorators, err := jsonString(n.Decorators)
	if err != nil {
		return err
	}
	typeParams, err := jsonString(n.TypeParameters)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO nodes(
			node_id, kind, name, qualified_name, file_path, language,
			start_line, end_line, start_column, end_column, docstring,
			signature, visibility, is_exported, is_async, is_static,
			is_abstract, decorators_json, type_parameters_json, return_type, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			kind=excluded.kind, name=excluded.name, qualified_name=excluded.qualified_name,
			file_path=excluded.file_path, language=excluded.language,
			start_line=excluded.start_line, end_line=excluded.end_line,
			start_column=excluded.start_column, end_column=excluded.end_column,
			docstring=excluded.docstring, signature=excluded.signature,
			visibility=excluded.visibility, is_exported=excluded.is_exported,
			is_async=excluded.is_async, is_static=excluded.is_static,
			is_abstract=excluded.is_abstract, decorators_json=excluded.decorators_json,
			type_parameters_json=excluded.type_parameters_json,
			return_type=excluded.return_type, updated_at=excluded.updated_at`,
		n.ID, n.Kind, n.Name, n.QualifiedName, n.FilePath, n.Language,
		n.StartLine, n.EndLine, n.StartColumn, n.EndColumn, n.Docstring,
		n.Signature, n.Visibility, boolInt(n.IsExported), boolInt(n.IsAsync),
		boolInt(n.IsStatic), boolInt(n.IsAbstract), decorators, typeParams,
		n.ReturnType, time.Now().Unix())
	return err
}

func insertEdge(ctx context.Context, tx *sql.Tx, e codeindex.Edge) error {
	metadata, err := jsonString(e.Metadata)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO edges(source, target, kind, line, column, provenance, metadata_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, target, kind, line, column) DO UPDATE SET
			provenance=excluded.provenance, metadata_json=excluded.metadata_json`,
		e.Source, e.Target, e.Kind, e.Line, e.Column, e.Provenance, metadata)
	return err
}

func jsonString(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	switch s := v.(type) {
	case []string:
		if len(s) == 0 {
			return "", nil
		}
	case map[string]any:
		if len(s) == 0 {
			return "", nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
