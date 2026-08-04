package main

import (
	"strings"
	"testing"
)

// B6: the root command's headless run-mode flags must not be silently
// ignored when combined with `lcoder rpc`.
func TestValidateRPCModeFlags(t *testing.T) {
	cases := []struct {
		name        string
		goal        string
		prompt      string
		json        bool
		wantErrPart string
	}{
		{"none set", "", "", false, ""},
		{"goal", "fix it", "", false, "--goal"},
		{"prompt", "", "hi", false, "--prompt"},
		{"json", "", "", true, "--json"},
		{"goal wins over prompt", "g", "p", true, "--goal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRPCModeFlags(tc.goal, tc.prompt, tc.json)
			if tc.wantErrPart == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Fatalf("error = %v, want mention of %s", err, tc.wantErrPart)
			}
		})
	}
}
