package api

import (
	"errors"
	"testing"
)

func TestConnectorManifestContract(t *testing.T) {
	manifest := RecommendedConnectorManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("shipped manifest invalid: %v", err)
	}
	if manifest.Version != ConnectorManifestVersion || len(manifest.Entries) < 10 {
		t.Fatalf("manifest: %+v", manifest)
	}
	for _, entry := range manifest.Entries {
		if entry.Admission != AdmissionOptIn {
			t.Fatalf("entry %s not opt-in", entry.Family)
		}
	}

	fresh := func() ConnectorManifest {
		return ConnectorManifest{Schema: SchemaVersion, Version: "1.0.0",
			Entries: []ConnectorRecommendation{{Family: "x1", Source: "src", Admission: AdmissionOptIn, Status: "recommended"}}}
	}
	for name, mutate := range map[string]func(*ConnectorManifest){
		"schema":     func(m *ConnectorManifest) { m.Schema = 9 },
		"version":    func(m *ConnectorManifest) { m.Version = "dev" },
		"empty":      func(m *ConnectorManifest) { m.Entries = nil },
		"dup family": func(m *ConnectorManifest) { m.Entries = append(m.Entries, m.Entries[0]) },
		"bad family": func(m *ConnectorManifest) { m.Entries[0].Family = "X1" },
		"no source":  func(m *ConnectorManifest) { m.Entries[0].Source = "" },
		"bad status": func(m *ConnectorManifest) { m.Entries[0].Status = "shipped" },
		"empty alt":  func(m *ConnectorManifest) { m.Entries[0].Alternatives = []string{"a", ""} },
		"many alts": func(m *ConnectorManifest) {
			m.Entries[0].Alternatives = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
		},
	} {
		m := fresh()
		mutate(&m)
		if err := m.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// a manifest entry that grants instead of recommending is an authority violation
	bad := fresh()
	bad.Entries[0].Admission = "granted"
	if err := bad.Validate(); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("granted admission: %v", err)
	}
}
