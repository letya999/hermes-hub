package contract

import (
	"errors"
	"testing"
)

func TestOptionalExtrasCannotExpandDeliveryAuthority(t *testing.T) {
	var absent *Extra
	if err := absent.Validate(); err != nil {
		t.Fatal(err)
	}
	valid := func() Extra {
		return Extra{
			Fields:     []Field{{ID: "account", Label: "Account", Kind: "string", MaxBytes: 128}},
			Deliveries: []Delivery{{Type: "env", Field: "account", Target: "ACCOUNT"}},
		}
	}
	allowed := valid()
	if err := allowed.Validate(); err != nil {
		t.Fatalf("optional bounded env input rejected: %v", err)
	}
	for _, change := range []struct {
		name  string
		apply func(*Extra)
	}{
		{"required input", func(e *Extra) { e.Fields[0].Required = true }},
		{"file delivery", func(e *Extra) { e.Deliveries[0].Type = "file" }},
		{"argument delivery", func(e *Extra) { e.Deliveries[0].Type = "argv" }},
		{"undelivered input", func(e *Extra) { e.Deliveries = nil }},
		{"duplicate input", func(e *Extra) { e.Fields = append(e.Fields, e.Fields[0]) }},
		{"duplicate delivery", func(e *Extra) { e.Deliveries = append(e.Deliveries, e.Deliveries[0]) }},
		{"unknown input", func(e *Extra) { e.Deliveries[0].Field = "unreviewed" }},
		{"environment assignment", func(e *Extra) { e.Deliveries[0].Target = "ACCOUNT=override" }},
		{"too many inputs", func(e *Extra) { e.Fields = make([]Field, MaxExtraInputs+1) }},
	} {
		e := valid()
		change.apply(&e)
		if err := e.Validate(); !errors.Is(err, ErrContract) {
			t.Errorf("%s did not fail closed: %v", change.name, err)
		}
	}
}
