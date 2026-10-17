package broker

import (
	"errors"
	"strconv"
	"testing"

	v1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/credential-broker/contract"
)

func TestExtraFieldsRejected(t *testing.T) {
	f := newFixture(t)
	base := v1.CreateRequest{ContractID: "pat", ContractRevision: 1, ConnectionID: "bad_extra", OnboardingID: "onboard_1", OwnerKind: "user", IdempotencyKey: "extra_base"}
	cases := map[string]*contract.Extra{
		"required_field": {Fields: []contract.Field{{ID: "x1", Kind: "string", Label: "X", Required: true, MaxBytes: 8}}, Deliveries: []contract.Delivery{{Type: "env", Field: "x1", Target: "X1_ENV"}}},
		"contract_field": {Fields: []contract.Field{{ID: "token", Kind: "string", Label: "X", MaxBytes: 8}}, Deliveries: []contract.Delivery{{Type: "env", Field: "token", Target: "X2_ENV"}}},
		"file_delivery":  {Fields: []contract.Field{{ID: "x3", Kind: "blob", Label: "X", MaxBytes: 8}}, Deliveries: []contract.Delivery{{Type: "file", Field: "x3", Target: "/run/secrets/x"}}},
		"target_clash":   {Fields: []contract.Field{{ID: "x4", Kind: "string", Label: "X", MaxBytes: 8}}, Deliveries: []contract.Delivery{{Type: "env", Field: "x4", Target: "API_TOKEN"}}},
		"no_delivery":    {Fields: []contract.Field{{ID: "x5", Kind: "string", Label: "X", MaxBytes: 8}}},
		"bad_field":      {Fields: []contract.Field{{ID: "BAD ID", Kind: "string", Label: "X", MaxBytes: 8}}, Deliveries: []contract.Delivery{{Type: "env", Field: "BAD ID", Target: "X6_ENV"}}},
	}
	for name, extra := range cases {
		in := base
		in.IdempotencyKey = "extra__" + name
		in.Extra = extra
		if _, e := f.b.CreateRequest(testCtx, alice, in); !errors.Is(e, ErrInvalid) {
			t.Fatalf("%s: %v", name, e)
		}
	}
	many := &contract.Extra{}
	for i := 0; i < contract.MaxExtraInputs+1; i++ {
		id := "x" + strconv.Itoa(i)
		many.Fields = append(many.Fields, contract.Field{ID: id, Kind: "string", Label: id, MaxBytes: 8})
		many.Deliveries = append(many.Deliveries, contract.Delivery{Type: "env", Field: id, Target: "EXTRA_ENV_" + id})
	}
	in := base
	in.IdempotencyKey = "extra__many"
	in.Extra = many
	if _, e := f.b.CreateRequest(testCtx, alice, in); !errors.Is(e, ErrInvalid) {
		t.Fatal("extras must be bounded", e)
	}
}
