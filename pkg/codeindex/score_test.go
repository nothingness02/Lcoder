package codeindex

import (
	"strings"
	"testing"
)

func TestExpandIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"RunAgent", []string{"runagent", "run", "agent"}},
		{"foo_bar", []string{"foo_bar", "foobar", "foo", "bar"}},
		{"HTTPServer", []string{"httpserver", "http", "server"}},
		{"parseXMLDocument", []string{"parsexmldocument", "parse", "xml", "document"}},
	}
	for _, tc := range tests {
		got := ExpandIdentifier(tc.input)
		m := make(map[string]bool, len(got))
		for _, g := range got {
			m[g] = true
		}
		for _, w := range tc.want {
			if !m[w] {
				t.Errorf("ExpandIdentifier(%q) missing %q, got %v", tc.input, w, got)
			}
		}
	}
}

func TestScoreNodeFieldWeights(t *testing.T) {
	q := Query{Keywords: []string{"run"}}
	nameHit := Node{Kind: NodeKindFunction, Name: "RunAgent"}
	docHit := Node{Kind: NodeKindFunction, Name: "Something", Docstring: "run this process"}
	if ScoreNode(nameHit, q) <= ScoreNode(docHit, q) {
		t.Fatalf("name hit should score higher than docstring hit")
	}
}

func TestScoreNodePrefixAndSuffix(t *testing.T) {
	q := Query{Keywords: []string{"run"}}
	prefix := Node{Kind: NodeKindFunction, Name: "RunFast"}
	suffix := Node{Kind: NodeKindMethod, Name: "FastRun", QualifiedName: "Manager.Run"}
	plain := Node{Kind: NodeKindFunction, Name: "Grunner"}
	sp := ScoreNode(prefix, q)
	ss := ScoreNode(suffix, q)
	so := ScoreNode(plain, q)
	if sp <= so {
		t.Fatalf("prefix match should beat plain substring, got prefix=%f plain=%f", sp, so)
	}
	if ss <= so {
		t.Fatalf("suffix match should beat plain substring, got suffix=%f plain=%f", ss, so)
	}
}

func TestScoreNodeKindFilter(t *testing.T) {
	q := Query{Keywords: []string{"user"}, Kinds: []NodeKind{NodeKindFunction}}
	fn := Node{Kind: NodeKindFunction, Name: "UserLoad"}
	st := Node{Kind: NodeKindStruct, Name: "User"}
	if ScoreNode(fn, q) == 0 {
		t.Fatal("function kind should match filter")
	}
	if ScoreNode(st, q) != 0 {
		t.Fatal("struct kind should be excluded by filter")
	}
}

func TestScoreNodeExactSymbol(t *testing.T) {
	q := Query{Keywords: []string{"run"}, Symbols: []string{"RunAgent"}}
	exact := Node{Kind: NodeKindFunction, Name: "RunAgent"}
	keyword := Node{Kind: NodeKindFunction, Name: "RunAgent"}
	se := ScoreNode(exact, q)
	sk := ScoreNode(keyword, Query{Keywords: []string{"run"}})
	if se <= sk {
		t.Fatalf("exact symbol query should outrank keyword query, exact=%f keyword=%f", se, sk)
	}
}

func TestNormalizeScores(t *testing.T) {
	results := []Result{
		{Relevance: 80},
		{Relevance: 40},
		{Relevance: 20},
	}
	NormalizeScores(results)
	if results[0].Relevance != 1.0 {
		t.Fatalf("top score should normalize to 1.0, got %f", results[0].Relevance)
	}
	if results[1].Relevance != 0.5 {
		t.Fatalf("second score should be 0.5, got %f", results[1].Relevance)
	}
}

func TestParseQuery(t *testing.T) {
	phrase, kws := ParseQuery("RunAgent daemon start")
	if phrase != "runagent daemon start" {
		t.Fatalf("phrase lowercasing failed: %q", phrase)
	}
	joined := " " + strings.Join(kws, " ") + " "
	for _, expected := range []string{"runagent", "run", "agent", "daemon", "start"} {
		if !strings.Contains(joined, " "+expected+" ") {
			t.Fatalf("expected keyword %q in %v", expected, kws)
		}
	}
}
