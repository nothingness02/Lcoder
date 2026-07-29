package skills

import (
	"os"

	"gopkg.in/yaml.v3"
)

// disabledFile is the on-disk shape of ~/.lcoder/skills.yaml, where the TUI
// skills panel persists user toggles.
type disabledFile struct {
	Disabled []string `yaml:"disabled"`
}

// LoadDisabledFile reads the persisted disabled list. A missing file is not
// an error (nothing disabled).
func LoadDisabledFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f disabledFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return f.Disabled, nil
}

// SaveDisabledFile persists the disabled list.
func SaveDisabledFile(path string, disabled []string) error {
	out, err := yaml.Marshal(disabledFile{Disabled: disabled})
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
