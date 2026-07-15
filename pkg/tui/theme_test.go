package tui

import (
	"testing"

	"github.com/lcoder/lcoder/pkg/tui/components"
)

func TestAccentResolves(t *testing.T) {
	// styleAccent must produce a non-empty render for non-empty input.
	if out := styleAccent().Render("x"); out == "" {
		t.Fatal("accent render empty")
	}
}

func TestApplyAccentSwapsColor(t *testing.T) {
	orig := colorAccent
	origComponents := components.ColorAccent
	defer func() {
		colorAccent = orig
		components.ColorAccent = origComponents
	}()

	applyAccent(accentPreset{name: "sunset", light: "#FF9C5C", dark: "#C95A10"})

	if colorAccent == orig {
		t.Fatal("applyAccent did not change colorAccent")
	}
}

func TestApplyAccentUpdatesCanonicalToken(t *testing.T) {
	orig := colorAccent
	origComponents := components.ColorAccent
	defer func() {
		colorAccent = orig
		components.ColorAccent = origComponents
	}()

	applyAccent(accentPreset{name: "sunset", light: "#FF9C5C", dark: "#C95A10"})

	if colorAccent == orig {
		t.Fatal("applyAccent did not change local colorAccent")
	}
	if components.ColorAccent == origComponents {
		t.Fatal("applyAccent did not change components.ColorAccent")
	}
	if colorAccent != components.ColorAccent {
		t.Fatalf("local and canonical accent diverged: %v vs %v", colorAccent, components.ColorAccent)
	}
}

func TestIsDarkBackgroundStable(t *testing.T) {
	a := isDarkBackground()
	b := isDarkBackground()
	if a != b {
		t.Fatal("isDarkBackground not stable across calls")
	}
}
