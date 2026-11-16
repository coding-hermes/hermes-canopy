package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestDBUpGaugeRecordsReadinessBranches(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetricsWith(reg)

	m.SetDBUp(true)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if got := gaugeValue(t, families, SeriesDBUp); got != 1 {
		t.Fatalf("healthy DB gauge = %v, want 1", got)
	}

	m.SetDBUp(false)
	families, err = reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if got := gaugeValue(t, families, SeriesDBUp); got != 0 {
		t.Fatalf("unavailable DB gauge = %v, want 0", got)
	}
}

func gaugeValue(t *testing.T, families []*dto.MetricFamily, name string) float64 {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %q not gathered", name)
	return -1
}
