package toolhub

import (
	"context"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type memoryVaultKey struct{}

type memoryVaultPin struct {
	gateway *Gateway
	auth    identity.Envelope
	root    string
}

// A cataloged memory call takes the owner lock before the registry fence;
// handlers reuse that held lock instead of acquiring it in the reverse order.
func (g *Gateway) withMemoryVault(ctx context.Context, auth identity.Envelope, fn func(string) error) error {
	if pin, ok := ctx.Value(memoryVaultKey{}).(memoryVaultPin); ok && pin.gateway == g && pin.auth.Equal(auth) {
		return fn(pin.root)
	}
	if g.Vaults == nil {
		return api.ErrIsolation
	}
	u, err := g.Vaults.Resolve(auth.PrincipalID, auth.Environment)
	if err != nil {
		return err
	}
	return u.Locked(fn)
}
