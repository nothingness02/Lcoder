package builtin

import "strings"

// This file implements the "model view vs disk bytes" convention shared by
// the read and edit tools (ported from kimi-code's line-endings.ts):
//
//   - Pure CRLF files are shown to the model with LF line endings; edits are
//     matched in that LF view and materialized back to CRLF on write, so the
//     model never has to reason about invisible carriage returns and bytes on
//     disk are never doubled or stripped.
//   - LF files are shown and edited as-is.
//   - Mixed line-ending files stay on the raw-byte exact-match path; the read
//     tool renders carriage returns as the literal two characters `\r` so the
//     model can see and reproduce them exactly.
type lineEndingStyle int

const (
	lineEndingLF lineEndingStyle = iota
	lineEndingCRLF
	lineEndingMixed
)

// detectLineEndingStyle classifies raw file bytes:
//   - every \n is part of a \r\n and no lone \r exists -> lineEndingCRLF
//   - no \r at all -> lineEndingLF
//   - anything else (lone \r, or \r\n coexisting with bare \n) -> lineEndingMixed
func detectLineEndingStyle(raw string) lineEndingStyle {
	crlf := strings.Count(raw, "\r\n")
	loneCR := strings.Count(raw, "\r") - crlf
	loneLF := strings.Count(raw, "\n") - crlf
	switch {
	case crlf > 0 && loneCR == 0 && loneLF == 0:
		return lineEndingCRLF
	case crlf == 0 && loneCR == 0:
		return lineEndingLF
	default:
		return lineEndingMixed
	}
}

// toModelTextView converts raw file bytes into the shared model view.
func toModelTextView(raw string, style lineEndingStyle) string {
	if style == lineEndingCRLF {
		return strings.ReplaceAll(raw, "\r\n", "\n")
	}
	return raw
}

// materializeModelText converts edited model-view text back to disk bytes.
// Only pure CRLF files are converted, and exactly once, so \r is never doubled.
func materializeModelText(view string, style lineEndingStyle) string {
	if style == lineEndingCRLF {
		return strings.ReplaceAll(view, "\n", "\r\n")
	}
	return view
}

// makeCarriageReturnsVisible renders CR characters as the literal two-character
// sequence `\r`. Used by the read tool for mixed line-ending files so the model
// can reproduce them byte-exactly in edit oldText.
func makeCarriageReturnsVisible(text string) string {
	return strings.ReplaceAll(text, "\r", `\r`)
}
