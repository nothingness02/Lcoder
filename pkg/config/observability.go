package config

import (
	"fmt"
	"os"

	"github.com/lcoder/lcoder/internal/paths"
	"gopkg.in/yaml.v3"
)

// ExporterConfig selects and configures an observability exporter.
type ExporterConfig struct {
	Type    string         `yaml:"type"`
	Output  string         `yaml:"output"`
	Options map[string]any `yaml:"options"`
}

// AuditConfig controls the security audit log.
type AuditConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// ContextSnapshotsConfig controls full context snapshot capture for manual
// testing and debugging. It is disabled by default to avoid production overhead.
type ContextSnapshotsConfig struct {
	Enabled             bool   `yaml:"enabled"`
	OutputDir           string `yaml:"output_dir"`
	MaxMessagesPerBlock int    `yaml:"max_messages_per_block"`
}

// SamplingConfig controls runtime sampling of observability records.
type SamplingConfig struct {
	Enabled bool    `yaml:"enabled"`
	Rate    float64 `yaml:"rate"`
}

// ObservabilityConfig holds all observability-related settings for an agent run.
type ObservabilityConfig struct {
	Exporter          ExporterConfig           `yaml:"exporter"`
	Audit             AuditConfig              `yaml:"audit"`
	Sampling          SamplingConfig           `yaml:"sampling"`
	ContextSnapshots  ContextSnapshotsConfig   `yaml:"context_snapshots"`
}

// DefaultObservabilityConfig returns the default observability settings.
func DefaultObservabilityConfig() ObservabilityConfig {
	return ObservabilityConfig{
		Exporter: ExporterConfig{
			Type:    "file",
			Output:  "",
			Options: nil,
		},
		Audit: AuditConfig{
			Enabled: true,
			Path:    "",
		},
		Sampling: SamplingConfig{
			Enabled: true,
			Rate:    1.0,
		},
		ContextSnapshots: ContextSnapshotsConfig{
			Enabled: false,
		},
	}
}

// DefaultObservabilityPath returns the standard location for the observability
// configuration file.
func DefaultObservabilityPath() string {
	return paths.LCoderHome("observability.yaml")
}

// LoadObservabilityConfig reads observability settings from path. If path is
// empty, DefaultObservabilityPath is used. A missing file returns the default
// configuration without error.
func LoadObservabilityConfig(path string) (ObservabilityConfig, error) {
	cfg := DefaultObservabilityConfig()
	if path == "" {
		path = DefaultObservabilityPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read observability config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse observability config: %w", err)
	}
	return cfg, nil
}
