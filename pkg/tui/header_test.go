package tui

import (
	"strings"
	"testing"
)

func TestHeaderContainsMeta(t *testing.T) {
	h := headerInfo{model: "kimi-k2", cwd: "~/lcoder", version: "0.1"}
	out := renderHeader(h, headerTotalFrames, 80)
	if !strings.Contains(stripANSI(out), "kimi-k2") {
		t.Fatal("header missing model")
	}
	if !strings.Contains(stripANSI(out), "Lcoder") {
		t.Fatal("header missing brand name")
	}
}
