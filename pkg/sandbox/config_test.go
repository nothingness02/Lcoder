package sandbox

import (
	"strings"
	"testing"
)

func TestNewDefaultsToPassthrough(t *testing.T) {
	sb, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Name() != "passthrough" {
		t.Fatalf("expected passthrough, got %q", sb.Name())
	}
}

func TestNewSoftLimit(t *testing.T) {
	sb, err := New(Config{
		Backend:     "soft-limit",
		ProjectRoot: t.TempDir(),
		Network:     NetworkConfig{Allow: []string{"api.example.com:443", "*.github.com:443"}},
		Filesystem:  FilesystemConfig{Readable: []string{"."}, Writable: []string{"."}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Name() != "soft-limit" {
		t.Fatalf("expected soft-limit, got %q", sb.Name())
	}
}

func TestNewReservedBackendsError(t *testing.T) {
	for _, b := range []string{"remote"} {
		_, err := New(Config{Backend: b})
		if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
			t.Fatalf("backend %q: expected not-implemented error, got %v", b, err)
		}
	}
}

func TestNewContainerBackend(t *testing.T) {
	sb, err := New(Config{Backend: "container", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sb.Name() != "container" {
		t.Fatalf("expected container, got %q", sb.Name())
	}
}

func TestNewUnknownBackendError(t *testing.T) {
	_, err := New(Config{Backend: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown sandbox backend") {
		t.Fatalf("expected unknown-backend error, got %v", err)
	}
}

func TestParseAllowEntry(t *testing.T) {
	e, err := parseAllowEntry("api.example.com:443")
	if err != nil || e.host != "api.example.com" || e.port != 443 {
		t.Fatalf("parse host:port: %+v err=%v", e, err)
	}
	bare, err := parseAllowEntry("example.com")
	if err != nil || bare.host != "example.com" || bare.port != 0 {
		t.Fatalf("parse bare host: %+v err=%v", bare, err)
	}
}
