package observability

import (
	"fmt"

	"github.com/lcoder/lcoder/pkg/config"
)

// NewExporterFromConfig builds an exporter from the observability configuration.
// When sampling is enabled it returns a *SamplingExporter so the caller can
// adjust the rate at runtime. The returned *SamplingExporter is nil when sampling
// is disabled.
func NewExporterFromConfig(cfg config.ObservabilityConfig, sessionID string) (Exporter, *SamplingExporter, error) {
	output := cfg.Exporter.Output
	if output == "" {
		output = DefaultPath(sessionID)
	}

	registry := DefaultRegistry()
	inner, err := registry.Create(cfg.Exporter.Type, cfg.Exporter.Options, output)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s exporter: %w", cfg.Exporter.Type, err)
	}

	if !cfg.Sampling.Enabled {
		return inner, nil, nil
	}

	sampler := NewSamplingExporter(inner, cfg.Sampling.Rate)
	return sampler, sampler, nil
}

// NewAuditLoggerFromConfig builds an audit logger from the observability
// configuration. It returns nil when auditing is disabled.
func NewAuditLoggerFromConfig(cfg config.ObservabilityConfig, sessionID string) (AuditLogger, error) {
	if !cfg.Audit.Enabled {
		return nil, nil
	}

	path := cfg.Audit.Path
	if path == "" {
		path = DefaultAuditPath(sessionID)
	}
	return NewFileAuditLogger(path)
}
