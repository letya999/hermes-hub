package broker

import (
	"slices"

	"github.com/letya999/credential-broker/contract"
)

// requestContract renders the contract one request actually collects and
// delivers: the pinned reviewed contract extended by request-scoped optional
// extras, with owner-demoted required fields relaxed. The catalog contract is
// never mutated — demotion and extras live on the request and credential
// records only.
func requestContract(c contract.Contract, extra *contract.Extra, demoted map[string]bool) contract.Contract {
	out := cloneContract(c)
	if extra != nil {
		out.Fields = append(out.Fields, extra.Fields...)
		out.Deliveries = append(out.Deliveries, extra.Deliveries...)
	}
	for id := range demoted {
		for i := range out.Fields {
			if out.Fields[i].ID == id {
				out.Fields[i].Required = false
			}
		}
	}
	return out
}

// validateExtra bounds request-scoped fields to optional env-delivered inputs
// that cannot collide with or shadow the reviewed contract. Per-item rules and
// the extras cap live in Extra.Validate; here only overlap with the contract
// is rejection-worthy.
func validateExtra(c contract.Contract, extra *contract.Extra) error {
	if extra == nil || len(extra.Fields) == 0 && len(extra.Deliveries) == 0 {
		return nil
	}
	if c.OAuthProvider != "" || extra.Validate() != nil {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for _, f := range c.Fields {
		ids[f.ID] = true
	}
	targets := map[string]bool{}
	for _, d := range c.Deliveries {
		targets[d.Target] = true
	}
	for _, f := range extra.Fields {
		if ids[f.ID] {
			return ErrInvalid
		}
	}
	for _, d := range extra.Deliveries {
		if targets[d.Target] {
			return ErrInvalid
		}
	}
	return nil
}

func requiredField(c contract.Contract, id string) bool {
	for _, f := range c.Fields {
		if f.ID == id {
			return f.Required
		}
	}
	return false
}

func sortedDemoted(demoted map[string]bool) []string {
	if len(demoted) == 0 {
		return nil
	}
	out := make([]string, 0, len(demoted))
	for id := range demoted {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func demotedSet(demoted []string) map[string]bool {
	if len(demoted) == 0 {
		return nil
	}
	out := make(map[string]bool, len(demoted))
	for _, id := range demoted {
		out[id] = true
	}
	return out
}
