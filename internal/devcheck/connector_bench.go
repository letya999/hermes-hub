package devcheck

import (
	"fmt"
	"sort"
)

type connectorBenchSample struct {
	CreateMS    float64 `json:"create_ms"`
	HandshakeMS float64 `json:"handshake_ms"`
	InstallMS   float64 `json:"install_ms"`
	FirstCallMS float64 `json:"first_call_ms"`
	RemoveMS    float64 `json:"remove_ms"`
}

type connectorBenchPhase struct {
	Runs  int     `json:"runs"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	MaxMS float64 `json:"max_ms"`
}

type connectorBenchReport struct {
	Install   connectorBenchPhase `json:"install"`
	FirstCall connectorBenchPhase `json:"first_call"`
	Remove    connectorBenchPhase `json:"remove"`
}

func benchPhase(samples []connectorBenchSample, pick func(connectorBenchSample) float64) connectorBenchPhase {
	values := make([]float64, len(samples))
	for i, sample := range samples {
		values[i] = pick(sample)
	}
	sort.Float64s(values)
	n := len(values)
	return connectorBenchPhase{Runs: n, P50MS: values[(n-1)/2], P95MS: values[(n*95-1)/100], MaxMS: values[n-1]}
}

// connectorBenchVerdict enforces the #286 budgets: install p95 ≤15 s, first
// call p95 ≤2 s, remove p95 ≤5 s. A breach is a regression failure.
func connectorBenchVerdict(report connectorBenchReport) error {
	for _, phase := range []struct {
		name  string
		p95   float64
		limit float64
	}{
		{"install", report.Install.P95MS, 15000},
		{"first call", report.FirstCall.P95MS, 2000},
		{"remove", report.Remove.P95MS, 5000},
	} {
		if phase.p95 > phase.limit {
			return fmt.Errorf("%s p95 %.0fms exceeds %.0fms budget", phase.name, phase.p95, phase.limit)
		}
	}
	return nil
}
