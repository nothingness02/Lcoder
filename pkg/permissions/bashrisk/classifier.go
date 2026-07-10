package bashrisk

import (
	"path/filepath"
	"strings"

	"github.com/lcoder/lcoder/internal/strutil"
)

// RiskLevel indicates the danger of a bash command.
type RiskLevel int

const (
	RiskNone RiskLevel = iota
	RiskLow
	RiskHigh
)

// Category describes why a command is risky.
type Category string

const (
	CatFileWriteOutside Category = "file-write-outside"
	CatFileDelete       Category = "file-delete"
	CatNetwork          Category = "network"
	CatExternalCode     Category = "external-code"
	CatPrivilege        Category = "privilege"
	CatCredential       Category = "credential"
	CatPackageInstall   Category = "package-install"
)

// Report is the result of classifying a command.
type Report struct {
	Level      RiskLevel
	Categories []Category
}

// Classify analyzes command in the context of projectRoot.
func Classify(command, projectRoot string) Report {
	if command == "" {
		return Report{Level: RiskNone}
	}
	tokens := tokenize(command)
	cats := detectCategories(tokens, projectRoot)
	level := RiskNone
	if len(cats) > 0 {
		level = RiskHigh
	} else if projectRoot != "" && hasInProjectWrite(tokens, projectRoot) {
		level = RiskLow
	}
	return Report{Level: level, Categories: cats}
}

func hasInProjectWrite(tokens []string, projectRoot string) bool {
	if len(tokens) == 0 {
		return false
	}
	if !inSlice(tokens[0], []string{"cp", "mv", "touch", "tee"}) {
		return false
	}
	for _, arg := range tokens[1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if !isOutsideProject(arg, projectRoot) {
			return true
		}
	}
	return false
}

func tokenize(s string) []string {
	var tokens []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	flush()
	return tokens
}

func detectCategories(tokens []string, projectRoot string) []Category {
	var cats []Category
	if len(tokens) == 0 {
		return cats
	}

	cmd := tokens[0]
	text := strings.Join(tokens, " ")

	// Privilege / system.
	if inSlice(cmd, []string{"sudo", "su", "doas", "pkexec"}) ||
		inSlice(cmd, []string{"systemctl", "service", "reboot", "shutdown", "halt", "poweroff", "fdisk", "mkfs", "dd"}) ||
		strings.HasPrefix(text, "chmod -R 777 /") ||
		strings.HasPrefix(text, "chown -R root /") {
		cats = appendDistinct(cats, CatPrivilege)
	}

	// Network / external code.
	if inSlice(cmd, []string{"curl", "wget", "nc", "ncat", "ssh", "scp", "git"}) {
		if inSlice(cmd, []string{"curl", "wget", "nc", "ncat", "ssh", "scp"}) ||
			strutil.ContainsAny(text, []string{"git clone", "git push", "git fetch", "git pull"}) {
			cats = appendDistinct(cats, CatNetwork)
		}
	}
	if strutil.ContainsAny(text, []string{"| bash", "| sh", "| python", "| python3", "bash -c", "python -c", "python3 -c", "eval ", "source "}) {
		cats = appendDistinct(cats, CatExternalCode)
	}

	// Package install.
	if inSlice(cmd, []string{"apt", "apt-get", "yum", "dnf", "pacman", "brew"}) ||
		strutil.ContainsAny(text, []string{"npm install -g", "pip install", "go install", "cargo install"}) {
		cats = appendDistinct(cats, CatPackageInstall)
	}

	// File delete / destructive.
	if inSlice(cmd, []string{"rm", "rmdir"}) || strutil.ContainsAny(text, []string{"git clean", "git reset --hard"}) {
		cats = appendDistinct(cats, CatFileDelete)
	}

	// File write outside project.
	if inSlice(cmd, []string{"cp", "mv", "touch", "tee"}) {
		for _, arg := range tokens[1:] {
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if isOutsideProject(arg, projectRoot) {
				cats = appendDistinct(cats, CatFileWriteOutside)
				break
			}
		}
	}

	// Credential access.
	if strutil.ContainsAny(text, []string{"~/.ssh", "~/.aws", "~/.gnupg", ".env", ".key", ".pem"}) {
		cats = appendDistinct(cats, CatCredential)
	}

	return cats
}

func isOutsideProject(arg, projectRoot string) bool {
	if projectRoot == "" {
		return false
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func inSlice(v string, s []string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func appendDistinct(cats []Category, c Category) []Category {
	for _, x := range cats {
		if x == c {
			return cats
		}
	}
	return append(cats, c)
}
