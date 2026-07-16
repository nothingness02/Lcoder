package main

import "testing"

func TestTrimInput(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"  hello\n", "hello"},
		{"\tworld ", "world"},
		{"no trim", "no trim"},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := trimInput(c.in); got != c.want {
			t.Fatalf("trimInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
