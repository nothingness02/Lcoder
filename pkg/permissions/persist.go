package permissions

import (
	"fmt"
	"os"

	"github.com/lcoder/lcoder/internal/fsutil"
	"gopkg.in/yaml.v3"
)

// ruleFile is the on-disk layout of a learned-rules file.
type ruleFile struct {
	Permissions struct {
		Rules map[string]map[string]string `yaml:"rules"`
	} `yaml:"permissions"`
}

// SaveRule writes or updates a permission rule in a YAML file. Patterns are
// arbitrary strings (paths with dots, command globs), so the file is edited
// as a plain map — a dotted pattern must never become a nested key path.
// A syntactically broken existing file is an error, never silently
// overwritten: learned rules the user already approved must not be lost.
func SaveRule(path, tool, pattern string, decision Decision) error {
	var doc ruleFile
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("refusing to overwrite unreadable rules file %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// new file, start empty
	default:
		return err
	}
	if doc.Permissions.Rules == nil {
		doc.Permissions.Rules = make(map[string]map[string]string)
	}
	if doc.Permissions.Rules[tool] == nil {
		doc.Permissions.Rules[tool] = make(map[string]string)
	}
	doc.Permissions.Rules[tool][pattern] = string(decision)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return fsutil.WritePrivateFile(path, out)
}
