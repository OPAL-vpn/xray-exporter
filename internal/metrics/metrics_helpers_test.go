package metrics

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/OPAL-vpn/xray-exporter/internal/config"
)

// sample is one gathered series: its family name, labels and value.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// labels builds a label set from alternating name/value pairs.
func labels(pairs ...string) map[string]string {
	if len(pairs)%2 != 0 {
		panic("labels: odd number of arguments")
	}
	out := make(map[string]string, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

// gatherSamples renders the snapshot through a fresh registry, the way
// Prometheus sees it, and flattens the result into samples.
func gatherSamples(t *testing.T, snap *Snapshot) []sample {
	t.Helper()

	reg := prometheus.NewRegistry()
	reg.MustRegister(snap.Collector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var out []sample
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			lbls := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				lbls[pair.GetName()] = pair.GetValue()
			}
			out = append(out, sample{name: family.GetName(), labels: lbls, value: sampleValue(metric)})
		}
	}
	return out
}

func sampleValue(metric *dto.Metric) float64 {
	switch {
	case metric.GetGauge() != nil:
		return metric.GetGauge().GetValue()
	case metric.GetCounter() != nil:
		return metric.GetCounter().GetValue()
	case metric.GetUntyped() != nil:
		return metric.GetUntyped().GetValue()
	default:
		return math.NaN()
	}
}

// findSample returns the value of the series with exactly this name and label
// set. It fails when more than one series matches.
func findSample(t *testing.T, samples []sample, name string, want map[string]string) (float64, bool) {
	t.Helper()

	var found []sample
	for _, s := range samples {
		if s.name == name && sameLabels(s.labels, want) {
			found = append(found, s)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%s%v: %d series, want at most one", name, want, len(found))
	}
	if len(found) == 0 {
		return 0, false
	}
	return found[0].value, true
}

// mustSample fails the test when the series is absent.
func mustSample(t *testing.T, samples []sample, name string, want map[string]string) float64 {
	t.Helper()

	got, ok := findSample(t, samples, name, want)
	if !ok {
		t.Fatalf("%s%v: series missing", name, want)
	}
	return got
}

// mustNoSample fails the test when the series is present.
func mustNoSample(t *testing.T, samples []sample, name string, want map[string]string) {
	t.Helper()

	if got, ok := findSample(t, samples, name, want); ok {
		t.Fatalf("%s%v: series present with %v, want absent", name, want, got)
	}
}

func sameLabels(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return true
}

func samplesNamed(samples []sample, name string) []sample {
	var out []sample
	for _, s := range samples {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

func timestampSeconds(at time.Time) float64 {
	return float64(at.UnixNano()) / 1e9
}

func durationSeconds(d time.Duration) float64 {
	return float64(d) / float64(time.Second)
}

var (
	seedTime   = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	secondTime = seedTime.Add(time.Minute)
	thirdTime  = seedTime.Add(2 * time.Minute)
)

func resolverDoH(name, url string) config.Resolver {
	return config.Resolver{Name: name, DoHURL: url}
}

// String keeps failures readable when a sample set is printed.
func (s sample) String() string {
	return fmt.Sprintf("%s%v=%v", s.name, s.labels, s.value)
}
