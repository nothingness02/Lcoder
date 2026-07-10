package permissions

import (
	"fmt"
	"os"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	koanf "github.com/knadh/koanf/v2"
	"github.com/lcoder/lcoder/internal/fsutil"
)

// SaveRule writes or updates a permission rule in a YAML file.
// The file layout matches:
//
//   permissions:
//     rules:
//       bash:
//         "go test *": allow
func SaveRule(path, tool, pattern string, decision Decision) error {
	k := koanf.New(".")
	if _, err := os.Stat(path); err == nil {
		_ = k.Load(file.Provider(path), yaml.Parser())
	}

	key := fmt.Sprintf("permissions.rules.%s.%s", tool, pattern)
	if err := k.Set(key, string(decision)); err != nil {
		return err
	}

	data, err := k.Marshal(yaml.Parser())
	if err != nil {
		return err
	}
	return fsutil.WritePrivateFile(path, data)
}
