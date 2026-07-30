package builtin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/lcoder/lcoder/pkg/models"
	"github.com/lcoder/lcoder/pkg/tools"
)

// Grep searches file contents for a pattern. It shells out to ripgrep when
// available (gitignore-aware, full glob/context/type/multiline support) and
// falls back to a built-in pure-Go search otherwise.
type Grep struct {
	cwd string
}

// NewGrep creates a grep tool.
func NewGrep(cwd string) tools.Executable {
	return &Grep{cwd: cwd}
}

func (g *Grep) Definition() models.ToolDefinition {
	return models.ToolDefinition{
		Name: "grep",
		Description: "Search file contents for a regular expression. Uses ripgrep when available (respects .gitignore); " +
			"otherwise a built-in fallback is used and glob only matches file names, context/type/multiline are ignored.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regular expression to search for",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory or file to search (default cwd)",
				},
				"glob": map[string]any{
					"type":        "string",
					"description": "Glob pattern to filter files, e.g. '*.go' or 'src/**/*.go' (path patterns require ripgrep)",
				},
				"output_mode": map[string]any{
					"type": "string",
					"enum": []string{"content", "files_with_matches", "count"},
					"description": "content (default): matching lines as file:line:text. files_with_matches: paths only, most recently " +
						"modified first. count: per-file match counts.",
				},
				"ignore_case": map[string]any{
					"type":        "boolean",
					"description": "Case-insensitive search",
				},
				"context": map[string]any{
					"type":        "integer",
					"description": "Lines of context before and after each match (content mode, requires ripgrep)",
				},
				"before_context": map[string]any{
					"type":        "integer",
					"description": "Lines of context before each match (content mode, requires ripgrep)",
				},
				"after_context": map[string]any{
					"type":        "integer",
					"description": "Lines of context after each match (content mode, requires ripgrep)",
				},
				"type": map[string]any{
					"type":        "string",
					"description": "ripgrep file type filter, e.g. go, js, ts, py (requires ripgrep)",
				},
				"multiline": map[string]any{
					"type":        "boolean",
					"description": "Allow matches to span multiple lines (requires ripgrep)",
				},
				"head_limit": map[string]any{
					"type":        "integer",
					"description": "Return at most this many output lines after offset (default 250; 0 for unlimited)",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Skip this many output lines before returning results",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

type grepParams struct {
	pattern       string
	searchRoot    string
	glob          string
	mode          string
	ignoreCase    bool
	context       int
	beforeContext int
	afterContext  int
	fileType      string
	multiline     bool
	headLimit     int
	offset        int
}

// DeclareAccesses implements tools.AccessDeclarer: grep searches a tree read-only.
func (g *Grep) DeclareAccesses(args map[string]any) []tools.ToolAccess {
	root := g.cwd
	if v := tools.String(args, "path", ""); v != "" {
		root = v
	}
	return []tools.ToolAccess{{Op: tools.OpSearch, Path: resolveInCwd(g.cwd, root), Recursive: true}}
}

func (g *Grep) Execute(ctx context.Context, callID string, args map[string]any) (models.ToolExecutionResult, error) {
	pattern, err := tools.RequiredString(args, "pattern")
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	params := grepParams{
		pattern:       pattern,
		mode:          "content",
		ignoreCase:    tools.Bool(args, "ignore_case", false),
		context:       tools.Int(args, "context", 0),
		beforeContext: tools.Int(args, "before_context", 0),
		afterContext:  tools.Int(args, "after_context", 0),
		fileType:      tools.String(args, "type", ""),
		multiline:     tools.Bool(args, "multiline", false),
		headLimit:     tools.Int(args, "head_limit", 250),
		offset:        tools.Int(args, "offset", 0),
	}
	if v := tools.String(args, "output_mode", ""); v != "" {
		params.mode = v
	}
	switch params.mode {
	case "content", "files_with_matches", "count":
	default:
		return models.ToolExecutionResult{}, fmt.Errorf(
			"invalid output_mode %q: must be 'content', 'files_with_matches', or 'count'", params.mode)
	}
	params.glob = tools.String(args, "glob", "")

	path := g.cwd
	if v := tools.String(args, "path", ""); v != "" {
		path = v
	}
	params.searchRoot = resolveInCwd(g.cwd, path)

	var text string
	var total int
	if rg := rgBinaryPath(); rg != "" {
		text, total, err = g.executeRipgrep(ctx, rg, params)
	} else {
		text, total, err = g.executeFallback(params)
	}
	if err != nil {
		return models.ToolExecutionResult{}, err
	}

	return models.ToolExecutionResult{
		Content: []models.ContentPart{models.TextContent{Text: text}},
		Details: map[string]any{"path": params.searchRoot, "output_mode": params.mode, "matches": total},
	}, nil
}

// --- ripgrep backend ---

func (g *Grep) executeRipgrep(ctx context.Context, rg string, p grepParams) (string, int, error) {
	cmdArgs := []string{"--hidden"}
	cmdArgs = append(cmdArgs, vcsExcludeArgs()...)
	switch p.mode {
	case "files_with_matches":
		// --null separates paths with NUL so names containing ':' or
		// newlines parse unambiguously.
		cmdArgs = append(cmdArgs, "-l", "--null", "--max-columns", "500")
	case "content":
		// --with-filename pins the path column when searching a single file;
		// no --max-columns / --max-count here so matching text is never
		// silently dropped (paging happens post-hoc via offset/head_limit,
		// long lines are capped per line).
		cmdArgs = append(cmdArgs, "-n", "--null", "--with-filename")
	case "count":
		cmdArgs = append(cmdArgs, "-c", "--null", "--with-filename", "--max-columns", "500")
	}
	if p.glob != "" {
		for _, pattern := range splitGlobPatterns(p.glob) {
			cmdArgs = append(cmdArgs, "--glob", pattern)
		}
	}
	if p.ignoreCase {
		cmdArgs = append(cmdArgs, "-i")
	}
	if p.context > 0 {
		cmdArgs = append(cmdArgs, "-C", fmt.Sprintf("%d", p.context))
	}
	if p.beforeContext > 0 {
		cmdArgs = append(cmdArgs, "-B", fmt.Sprintf("%d", p.beforeContext))
	}
	if p.afterContext > 0 {
		cmdArgs = append(cmdArgs, "-A", fmt.Sprintf("%d", p.afterContext))
	}
	if p.fileType != "" {
		cmdArgs = append(cmdArgs, "--type", p.fileType)
	}
	if p.multiline {
		cmdArgs = append(cmdArgs, "-U", "--multiline-dotall")
	}
	// "--" keeps patterns starting with '-' from being parsed as flags.
	cmdArgs = append(cmdArgs, "--", p.pattern, p.searchRoot)

	stdout, stderr, capped, err := runSearchCommand(ctx, "", rg, cmdArgs...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "no matches found", 0, nil
		}
		return "", 0, classifySearchError("grep", stderr, err)
	}

	var lines []string
	switch p.mode {
	case "files_with_matches":
		for _, path := range strings.Split(stdout, "\x00") {
			if path = strings.TrimRight(path, "\r\n"); path != "" {
				lines = append(lines, path)
			}
		}
		sortFilesByMTime(lines)
		for i := range lines {
			lines[i] = relDisplayPath(g.cwd, lines[i])
		}
	case "content":
		for _, rec := range strings.Split(stdout, "\n") {
			rec = strings.TrimRight(rec, "\r")
			if rec == "" {
				continue
			}
			if rec == "--" { // context group separator
				lines = append(lines, rec)
				continue
			}
			if path, rest, ok := strings.Cut(rec, "\x00"); ok {
				rec = relDisplayPath(g.cwd, path) + ":" + rest
			}
			if len(rec) > maxGrepContentLineChars {
				rec = rec[:maxGrepContentLineChars] + "[...truncated]"
			}
			lines = append(lines, rec)
		}
	case "count":
		for _, rec := range strings.Split(stdout, "\n") {
			rec = strings.TrimRight(rec, "\r")
			if rec == "" {
				continue
			}
			if path, count, ok := strings.Cut(rec, "\x00"); ok {
				rec = relDisplayPath(g.cwd, path) + ":" + count
			}
			lines = append(lines, rec)
		}
	}

	return formatGrepResult(lines, p.offset, p.headLimit, capped), len(lines), nil
}

// --- built-in fallback backend (no ripgrep) ---

func (g *Grep) executeFallback(p grepParams) (string, int, error) {
	pattern := p.pattern
	if p.ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", 0, fmt.Errorf("invalid regex pattern %q: %v", p.pattern, err)
	}
	if p.glob != "" {
		if _, gerr := filepath.Match(p.glob, ""); gerr != nil {
			return "", 0, fmt.Errorf("invalid glob pattern %q: %v", p.glob, gerr)
		}
	}

	var lines []string
	counts := map[string]int{}
	seen := map[string]bool{}
	var skippedLarge int
	var walkErrs walkErrorLog
	limitReached := false
	err = filepath.WalkDir(p.searchRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			walkErrs.record(path, walkErr)
			return nil
		}
		if d.IsDir() {
			if isVCSDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if p.glob != "" {
			matched, _ := filepath.Match(p.glob, filepath.Base(path))
			if !matched {
				return nil
			}
		}
		info, statErr := d.Info()
		if statErr == nil && info.Size() > maxGrepFileSizeBytes {
			skippedLarge++
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			walkErrs.record(path, readErr)
			return nil
		}
		fileLines := strings.Split(string(data), "\n")
		for i, line := range fileLines {
			line = strings.TrimRight(line, "\r")
			if !re.MatchString(line) {
				continue
			}
			switch p.mode {
			case "content":
				if len(line) > maxGrepContentLineChars {
					line = line[:maxGrepContentLineChars] + "[...truncated]"
				}
				lines = append(lines, fmt.Sprintf("%s:%d:%s", relDisplayPath(g.cwd, path), i+1, line))
			case "files_with_matches":
				if !seen[path] {
					seen[path] = true
					lines = append(lines, path)
				}
			case "count":
				if !seen[path] {
					seen[path] = true
					lines = append(lines, path)
				}
				counts[path]++
			}
			if len(lines) >= maxGrepMatches && p.mode == "content" {
				limitReached = true
				return filepath.SkipAll
			}
			if p.mode == "files_with_matches" {
				break // one match per file is enough
			}
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}

	if p.mode == "files_with_matches" {
		sortFilesByMTime(lines)
	}
	for i, path := range lines {
		if p.mode == "count" {
			lines[i] = relDisplayPath(g.cwd, path) + ":" + fmt.Sprintf("%d", counts[path])
		} else if p.mode == "files_with_matches" {
			lines[i] = relDisplayPath(g.cwd, path)
		}
	}

	total := len(lines)
	text := formatGrepResult(lines, p.offset, p.headLimit, limitReached)
	if skippedLarge > 0 {
		text += fmt.Sprintf("\n[skipped %d file(s) larger than %d bytes]", skippedLarge, maxGrepFileSizeBytes)
	}
	text += walkErrs.notice()
	return text, total, nil
}

// --- shared helpers ---

// formatGrepResult applies the offset/head_limit window and appends
// truncation notices.
func formatGrepResult(lines []string, offset, headLimit int, truncated bool) string {
	total := len(lines)
	if total == 0 {
		return "no matches found"
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return fmt.Sprintf("[offset %d is beyond the %d result lines]", offset, total)
	}
	end := total
	if headLimit > 0 && offset+headLimit < end {
		end = offset + headLimit
	}
	text := strings.Join(lines[offset:end], "\n")
	if end < total {
		text += fmt.Sprintf("\n[truncated: showing %d of %d lines; use offset=%d to see more]", end-offset, total, end)
	}
	if truncated {
		text += "\n[output cap reached; results are incomplete — narrow the search with a more specific pattern, path, or glob]"
	}
	return text
}

// splitGlobPatterns splits a glob argument on whitespace and commas while
// keeping brace expansions like *.{ts,tsx} intact.
func splitGlobPatterns(glob string) []string {
	var out []string
	for _, raw := range strings.Fields(glob) {
		if strings.Contains(raw, "{") && strings.Contains(raw, "}") {
			out = append(out, raw)
			continue
		}
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// sortFilesByMTime sorts paths newest-first; ties keep path order.
func sortFilesByMTime(paths []string) {
	type entry struct {
		path  string
		mtime time.Time
	}
	entries := make([]entry, len(paths))
	for i, p := range paths {
		entries[i] = entry{path: p}
		if info, err := os.Stat(p); err == nil {
			entries[i].mtime = info.ModTime()
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].mtime.Equal(entries[j].mtime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].mtime.After(entries[j].mtime)
	})
	for i := range entries {
		paths[i] = entries[i].path
	}
}

// relDisplayPath relativizes a path against cwd for token-efficient output;
// paths outside cwd stay absolute so follow-up read/edit calls address the
// same file. Windows volume/path case differences are tolerated.
func relDisplayPath(cwd, path string) string {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}
	if rel, ok := relWithin(cwd, path); ok {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func relWithin(base, target string) (string, bool) {
	if runtime.GOOS == "windows" && len(target) > len(base) &&
		strings.EqualFold(target[:len(base)], base) &&
		(target[len(base)] == '\\' || target[len(base)] == '/') {
		return target[len(base)+1:], true
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

var _ tools.Executable = (*Grep)(nil)
