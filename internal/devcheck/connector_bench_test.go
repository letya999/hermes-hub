package devcheck

import (
	"strings"
	"testing"
)

func TestBenchPhasePercentiles(t *testing.T) {
	samples := make([]connectorBenchSample, 20)
	for i := range samples {
		samples[i] = connectorBenchSample{InstallMS: float64(i + 1)}
	}
	phase := benchPhase(samples, func(s connectorBenchSample) float64 { return s.InstallMS })
	if phase.P50MS != 10 || phase.P95MS != 19 || phase.MaxMS != 20 || phase.Runs != 20 {
		t.Fatalf("percentiles: %+v", phase)
	}
}

func TestConnectorBenchVerdict(t *testing.T) {
	inside := connectorBenchReport{
		Install:   connectorBenchPhase{P95MS: 3000},
		FirstCall: connectorBenchPhase{P95MS: 100},
		Remove:    connectorBenchPhase{P95MS: 200},
	}
	if err := connectorBenchVerdict(inside); err != nil {
		t.Fatalf("in-budget report rejected: %v", err)
	}
	for name, mutate := range map[string]func(*connectorBenchReport){
		"install":    func(r *connectorBenchReport) { r.Install.P95MS = 15001 },
		"first call": func(r *connectorBenchReport) { r.FirstCall.P95MS = 2001 },
		"remove":     func(r *connectorBenchReport) { r.Remove.P95MS = 5001 },
	} {
		over := inside
		mutate(&over)
		if err := connectorBenchVerdict(over); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s regression accepted: %v", name, err)
		}
	}
}
