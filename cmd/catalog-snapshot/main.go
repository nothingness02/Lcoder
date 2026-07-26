// Command catalog-snapshot regenerates pkg/llm/catalog/snapshot.json from a
// models.dev-style api.json. Run manually when refreshing the embedded
// catalog: go run ./cmd/catalog-snapshot [source-url]
package main

import (
	"encoding/json"
	"fmt"
	"os"

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
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	const out = "pkg/llm/catalog/snapshot.json"
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s: %d providers, %d models\n", out, len(ds.Providers), len(ds.Models))
}
