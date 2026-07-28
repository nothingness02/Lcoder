package builtin

import "testing"

func TestDetectLineEndingStyle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want lineEndingStyle
	}{
		{"empty", "", lineEndingLF},
		{"lf", "a\nb\n", lineEndingLF},
		{"lf no trailing newline", "a\nb", lineEndingLF},
		{"crlf", "a\r\nb\r\n", lineEndingCRLF},
		{"crlf no trailing newline", "a\r\nb", lineEndingCRLF},
		{"mixed crlf and lf", "a\r\nb\n", lineEndingMixed},
		{"lone cr", "a\rb\r\n", lineEndingMixed},
		{"old mac cr only", "a\rb\r", lineEndingMixed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectLineEndingStyle(tc.raw); got != tc.want {
				t.Fatalf("detectLineEndingStyle(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestModelViewRoundTrip(t *testing.T) {
	raw := "package main\r\n\r\nfunc main() {}\r\n"
	style := detectLineEndingStyle(raw)
	if style != lineEndingCRLF {
		t.Fatalf("style = %d, want CRLF", style)
	}
	view := toModelTextView(raw, style)
	if view != "package main\n\nfunc main() {}\n" {
		t.Fatalf("view = %q", view)
	}
	// Editing in the view and materializing restores CRLF without doubling.
	edited := "package main\n\nfunc main() { run() }\n"
	got := materializeModelText(edited, style)
	want := "package main\r\n\r\nfunc main() { run() }\r\n"
	if got != want {
		t.Fatalf("materialized = %q, want %q", got, want)
	}
	// Untouched text round-trips byte-identically.
	if materializeModelText(view, style) != raw {
		t.Fatal("round-trip should be byte-identical")
	}
}

func TestMaterializeLeavesLFAndMixedUntouched(t *testing.T) {
	for _, style := range []lineEndingStyle{lineEndingLF, lineEndingMixed} {
		text := "a\nb\r\nc"
		if got := materializeModelText(text, style); got != text {
			t.Fatalf("style %d: materialize modified text: %q", style, got)
		}
	}
}

func TestMakeCarriageReturnsVisible(t *testing.T) {
	got := makeCarriageReturnsVisible("a\r\nb\rc")
	want := `a\r` + "\n" + `b\rc`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
