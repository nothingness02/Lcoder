package observability

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// Sampler decides whether a single observability record should be exported.
// Implementations must be safe for concurrent use.
type Sampler func(Record) bool

// ProbabilisticSampler returns a sampler that keeps each record with the given
// probability. rate is clamped to [0, 1]. A nil source defaults to a source
// seeded from the current time.
func ProbabilisticSampler(rate float64, src rand.Source) Sampler {
	rate = math.Max(0, math.Min(1, rate))
	switch {
	case rate <= 0:
		return func(Record) bool { return false }
	case rate >= 1:
		return func(Record) bool { return true }
	}
	if src == nil {
		src = rand.NewSource(time.Now().UnixNano())
	}
	r := rand.New(src)
	var mu sync.Mutex
	return func(Record) bool {
		mu.Lock()
		defer mu.Unlock()
		return r.Float64() < rate
	}
}

// SamplingExporter wraps another Exporter and drops records according to a
// configurable Sampler. It is safe to call SetRate/SetSampler and Export from
// multiple goroutines.
type SamplingExporter struct {
	exporter Exporter
	mu       sync.RWMutex
	sampler  Sampler
}

// NewSamplingExporter creates a sampling exporter that keeps records with the
// given probability. A rate of 0 drops everything; a rate of 1 forwards
// everything.
func NewSamplingExporter(exporter Exporter, rate float64) *SamplingExporter {
	return NewSamplingExporterWithSampler(exporter, ProbabilisticSampler(rate, nil))
}

// NewSamplingExporterWithSampler creates a sampling exporter with a custom
// sampler. A nil sampler forwards all records.
func NewSamplingExporterWithSampler(exporter Exporter, sampler Sampler) *SamplingExporter {
	return &SamplingExporter{
		exporter: exporter,
		sampler:  sampler,
	}
}

// SetRate replaces the current sampler with a fresh probabilistic sampler using
// the given rate. This lets operators tune sampling without recreating the
// exporter stack.
func (e *SamplingExporter) SetRate(rate float64) {
	e.SetSampler(ProbabilisticSampler(rate, nil))
}

// SetSampler replaces the current sampler. Passing nil disables sampling and
// exports every record.
func (e *SamplingExporter) SetSampler(sampler Sampler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sampler = sampler
}

// Export forwards the record to the underlying exporter if the sampler accepts
// it. Dropped records are not considered errors.
func (e *SamplingExporter) Export(record Record) error {
	e.mu.RLock()
	sampler := e.sampler
	e.mu.RUnlock()

	if sampler != nil && !sampler(record) {
		return nil
	}
	return e.exporter.Export(record)
}

// Close delegates to the wrapped exporter.
func (e *SamplingExporter) Close() error {
	return e.exporter.Close()
}
