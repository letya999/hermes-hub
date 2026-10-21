package broker

import (
	"slices"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
)

// RegisterContract adds a dynamically registered contract to the catalog. The
// caller is an authenticated control-plane actor (ToolHub) synthesizing a
// contract from a reviewed connector definition; the user's form submission
// remains the consent boundary for the values it collects.
func (b *Broker) RegisterContract(a identity.Actor, c contract.Contract) (contract.Contract, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.live() {
		return contract.Contract{}, ErrUnavailable
	}
	if !a.Valid() {
		return contract.Contract{}, ErrInvalid
	}
	// ADR-0071 permits one bounded owner-only JSON session through the same
	// lease machinery; routes and provider implementations remain static.
	if c.OAuthProvider != "" || len(c.Routes) != 0 {
		return contract.Contract{}, ErrDenied
	}
	if c.State != nil && (c.AllowShared || len(c.State.Files) != 1 || !c.State.Files[0].JSON || c.State.Files[0].MaxBytes > 65536) {
		return contract.Contract{}, ErrDenied
	}
	if c.Storage == "" {
		ids := make([]string, 0, len(b.cfg.Providers))
		for id := range b.cfg.Providers {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if b.cfg.Providers[id].Capabilities().Write {
				c.Storage = id
				break
			}
		}
	}
	if c.Validate() != nil || b.cfg.Providers[c.Storage] == nil {
		return contract.Contract{}, ErrInvalid
	}
	k := key(c.ID, c.Revision)
	if existing, ok := b.catalog[k]; ok {
		if existing.Digest() == c.Digest() {
			return existing, nil
		}
		return contract.Contract{}, ErrConflict
	}
	e := b.audit("contract.registered", a)
	e.ContractID = k
	if _, err := b.commit(mutation{Audit: e, Contract: &contractRecord{Contract: c}}); err != nil {
		return contract.Contract{}, err
	}
	return c, nil
}
