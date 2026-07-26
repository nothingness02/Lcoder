package catalog

import (
	"encoding/json"
	"testing"
)

// The embedded snapshot must carry the current dataset format with provider
// metadata — wire inference (resolve.go) depends on it at startup, before any
// background refresh lands.
func TestEmbeddedSnapshotHasProviderMeta(t *testing.T) {
	var ds Dataset
	if err := json.Unmarshal(snapshotJSON, &ds); err != nil {
		t.Fatalf("snapshot is not a Dataset: %v", err)
	}
	if len(ds.Models) == 0 {
		t.Fatal("snapshot has no models")
	}
	byID := map[string]ProviderMeta{}
	for _, p := range ds.Providers {
		byID[p.ID] = p
	}
	for _, want := range []string{"anthropic", "openai"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("snapshot missing provider meta %q", want)
		}
	}
	// 过滤必须已生效:snapshot 里不应有 deprecated/embedding 条目
	for _, e := range ds.Models {
		if hasEmbeddingMarker(e.ID) {
			t.Errorf("snapshot contains embedding model %q", e.ID)
		}
	}
}
