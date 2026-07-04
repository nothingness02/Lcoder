package observability

import (
	"math/rand"
	"testing"
)

func TestSamplingExporterRateZeroDropsAll(t *testing.T) {
	inner := NewMemoryExporter()
	ex := NewSamplingExporter(inner, 0)
	defer ex.Close()

	for i := 0; i < 100; i++ {
		if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: float64(i)}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	if len(inner.Records) != 0 {
		t.Fatalf("rate 0 should drop everything, got %d records", len(inner.Records))
	}
}

func TestSamplingExporterRateOneKeepsAll(t *testing.T) {
	inner := NewMemoryExporter()
	ex := NewSamplingExporter(inner, 1)
	defer ex.Close()

	for i := 0; i < 100; i++ {
		if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: float64(i)}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	if len(inner.Records) != 100 {
		t.Fatalf("rate 1 should keep everything, got %d records", len(inner.Records))
	}
}

func TestSamplingExporterAdjustRate(t *testing.T) {
	inner := NewMemoryExporter()
	ex := NewSamplingExporter(inner, 0)
	defer ex.Close()

	if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 1}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(inner.Records) != 0 {
		t.Fatal("initial rate 0 should drop record")
	}

	ex.SetRate(1)
	if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: 2}}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(inner.Records) != 1 {
		t.Fatalf("after SetRate(1) record should be kept, got %d", len(inner.Records))
	}
}

func TestSamplingExporterProbabilisticApproximation(t *testing.T) {
	inner := NewMemoryExporter()
	// Seeded source makes the test deterministic while still exercising the
	// probabilistic path.
	ex := NewSamplingExporterWithSampler(inner, ProbabilisticSampler(0.5, rand.NewSource(42)))
	defer ex.Close()

	const n = 1000
	for i := 0; i < n; i++ {
		if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: float64(i)}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}

	got := len(inner.Records)
	// With a fair coin over 1000 flips, anything far from 50% is suspicious.
	if got < 400 || got > 600 {
		t.Fatalf("expected ~500 records with rate 0.5, got %d", got)
	}
}

func TestSamplingExporterClosePropagates(t *testing.T) {
	inner := NewMemoryExporter()
	ex := NewSamplingExporter(inner, 0.5)
	if err := ex.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestProbabilisticSamplerClamping(t *testing.T) {
	cases := []struct {
		rate float64
		want bool
	}{
		{-0.5, false},
		{0, false},
		{1, true},
		{1.5, true},
	}
	for _, c := range cases {
		s := ProbabilisticSampler(c.rate, rand.NewSource(1))
		got := s(Record{Type: "metric"})
		if got != c.want {
			t.Errorf("rate %v: got %v, want %v", c.rate, got, c.want)
		}
	}
}

func TestSamplingExporterNilSamplerForwardsAll(t *testing.T) {
	inner := NewMemoryExporter()
	ex := NewSamplingExporterWithSampler(inner, nil)
	defer ex.Close()

	for i := 0; i < 10; i++ {
		if err := ex.Export(Record{Type: "metric", Metric: &Metric{Name: "m", Value: float64(i)}}); err != nil {
			t.Fatalf("export: %v", err)
		}
	}
	if len(inner.Records) != 10 {
		t.Fatalf("nil sampler should forward all records, got %d", len(inner.Records))
	}
}
