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
	// (与 isUsableChatModel 对齐,ID 和 Name 都要检查)
	for _, e := range ds.Models {
		if hasEmbeddingMarker(e.ID) || hasEmbeddingMarker(e.Name) {
			t.Errorf("snapshot contains embedding model %q (%q)", e.ID, e.Name)
		}
	}
	// 排序是契约:生成工具必须输出确定性顺序(re-bake diff 最小)
	for i := 1; i < len(ds.Providers); i++ {
		if ds.Providers[i-1].ID >= ds.Providers[i].ID {
			t.Errorf("providers not sorted by ID: %q >= %q", ds.Providers[i-1].ID, ds.Providers[i].ID)
		}
	}
	for i := 1; i < len(ds.Models); i++ {
		prev, cur := ds.Models[i-1], ds.Models[i]
		if prev.Provider > cur.Provider ||
			(prev.Provider == cur.Provider && prev.ID >= cur.ID) {
			t.Errorf("models not sorted by (provider, id): %s/%s >= %s/%s",
				prev.Provider, prev.ID, cur.Provider, cur.ID)
		}
	}
}
