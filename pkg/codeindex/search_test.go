package codeindex

import (
	"fmt"
	"strings"
	"testing"
)

func TestSearchSnapshotHonorsKinds(t *testing.T) {
	snap := &Snapshot{
		Nodes: []Symbol{
			{ID: "f1", Kind: NodeKindFunction, Name: "RunAgent", FilePath: "a.go", StartLine: 1, Signature: "func RunAgent()"},
			{ID: "s1", Kind: NodeKindStruct, Name: "RunAgent", FilePath: "a.go", StartLine: 10, Signature: "type RunAgent struct{}"},
		},
	}
	res, err := SearchSnapshot(snap, Query{Keywords: []string{"runagent"}, Kinds: []NodeKind{NodeKindFunction}, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Node.Kind != NodeKindFunction {
		t.Fatalf("expected one function result, got %+v", res)
	}
}

func TestSearchSnapshotRanksExactFirst(t *testing.T) {
	snap := &Snapshot{
		Nodes: []Symbol{
			{ID: "runner", Kind: NodeKindFunction, Name: "Runner", FilePath: "a.go", StartLine: 1, Signature: "func Runner()"},
			{ID: "run", Kind: NodeKindFunction, Name: "Run", FilePath: "a.go", StartLine: 2, Signature: "func Run()"},
		},
	}
	res, err := SearchSnapshot(snap, Query{Keywords: []string{"run"}, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) < 2 {
		t.Fatal("expected both results")
	}
	if res[0].Node.Name != "Run" {
		t.Fatalf("expected exact match 'Run' first, got %s", res[0].Node.Name)
	}
	if res[0].Relevance != 1.0 {
		t.Fatalf("top result should normalize to 1.0, got %f", res[0].Relevance)
	}
}

func TestSearchSnapshotTruncatesBeforeStubFormat(t *testing.T) {
	var nodes []Symbol
	for i := 0; i < 20; i++ {
		nodes = append(nodes, Symbol{
			ID:        fmt.Sprintf("id-%d", i),
			Kind:      NodeKindFunction,
			Name:      fmt.Sprintf("Foo%d", i),
			FilePath:  "a.go",
			StartLine: i,
			Signature: strings.Repeat("func Foo() { /* long body */ }", 50),
		})
	}
	snap := &Snapshot{Nodes: nodes}
	res, err := SearchSnapshot(snap, Query{Keywords: []string{"foo"}, MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}
}
