package permissions

import "testing"

func TestLiteralCommandPattern(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		// 逐字保留:普通命令原样,绝不泛化。
		{"rm -rf /tmp/x", "rm -rf /tmp/x"},
		{"git status --short", "git status --short"},
		{"ls -la", "ls -la"},
		// glob 特殊字符转义为字符类,保证学到的规则只匹配命令本身。
		{"find . -name '*.log'", "find . -name '[*].log'"},
		{"ls a?b [x]", "ls a[?]b [[]x]"},
		{"", "*"},
	}
	for _, c := range cases {
		got := LiteralCommandPattern(c.cmd)
		if got != c.want {
			t.Errorf("%q: got %q, want %q", c.cmd, got, c.want)
		}
	}
}

// 学到的规则只命中命令本身,任何变体都不命中——多问一次,永不误放。
func TestLiteralCommandPatternMatchesOnlyItself(t *testing.T) {
	learned := LiteralCommandPattern("rm -rf /tmp/x")
	if !MatchCommand(learned, "rm -rf /tmp/x") {
		t.Fatal("literal pattern must match the original command")
	}
	for _, other := range []string{"rm -rf /tmp/y", "rm -rf /etc", "rm -rf /", "rm important.go"} {
		if MatchCommand(learned, other) {
			t.Fatalf("literal pattern must NOT match %q", other)
		}
	}

	// 含 glob 字符的命令:学到的规则也不能匹配"相似"命令。
	globby := LiteralCommandPattern("find . -name '*.log'")
	if !MatchCommand(globby, "find . -name '*.log'") {
		t.Fatal("escaped literal must match the original globby command")
	}
	if MatchCommand(globby, "find . -name 'x.log'") {
		t.Fatal("escaped literal must not let * act as a wildcard")
	}
}
