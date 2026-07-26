// Command catalog-snapshot regenerates pkg/llm/catalog/snapshot.json from a
// models.dev-style api.json. Run manually when refreshing the embedded
// catalog: go run ./cmd/catalog-snapshot [source-url]
//
// It must be run from the repository root: the output path is relative to
// the current working directory.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/lcoder/lcoder/pkg/llm/catalog"
)

func main() {
	url := "https://models.dev/api.json"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	ds, err := catalog.FetchEntries(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
	// FetchEntries builds from map iteration; sort so re-baking produces a
	// deterministic, minimal diff.
	sort.Slice(ds.Providers, func(i, j int) bool { return ds.Providers[i].ID < ds.Providers[j].ID })
	sort.Slice(ds.Models, func(i, j int) bool {
		if ds.Models[i].Provider != ds.Models[j].Provider {
			return ds.Models[i].Provider < ds.Models[j].Provider
		}
		return ds.Models[i].ID < ds.Models[j].ID
	})
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	// Atomic write: temp file in the same directory, then rename, so a crash
	// mid-write never leaves a truncated snapshot.
	const out = "pkg/llm/catalog/snapshot.json"
	tmp := out + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, out); err != nil {
		fmt.Fprintln(os.Stderr, "rename:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: %d providers, %d models\n", out, len(ds.Providers), len(ds.Models))
}
