// Package toolsurface defines the v1 owner-scoped tool catalog read contract.
package toolsurface

import (
	"context"
	"net/http"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	"github.com/letya999/hermes-hub/internal/identity"
)

var (
	ErrNotFound    = wire.ErrNotFound
	ErrConflict    = wire.ErrConflict
	ErrForbidden   = wire.ErrForbidden
	ErrInvalid     = wire.ErrInvalid
	ErrUnavailable = wire.ErrUnavailable
)

type Transport string
type WorkloadClass string

type CatalogEntry struct {
	Name               string        `json:"name"`
	Version            string        `json:"version"`
	Transport          Transport     `json:"transport"`
	WorkloadClass      WorkloadClass `json:"workload_class"`
	Tools              []string      `json:"tools"`
	BindingID          string        `json:"binding_id,omitempty"`
	Status             string        `json:"status"`
	Enabled            bool          `json:"enabled"`
	MissingCredentials []string      `json:"missing_credentials,omitempty"`
}

type Client interface {
	Catalog(context.Context, identity.Envelope) ([]CatalogEntry, error)
}

type inProc struct{ server Client }

func NewInProc(server Client) Client { return inProc{server} }

func (c inProc) Catalog(ctx context.Context, envelope identity.Envelope) ([]CatalogEntry, error) {
	if err := envelope.Validate(envelope.PrincipalID, envelope.ContextID, envelope.RuntimeID, envelope.PolicyVersion); err != nil {
		return nil, ErrForbidden
	}
	envelope, err := wire.Copy(envelope)
	if err != nil {
		return nil, err
	}
	entries, err := c.server.Catalog(ctx, envelope)
	if err != nil {
		return nil, wire.Normalize(err)
	}
	return wire.Copy(entries)
}

type httpClient struct{ wire.Client }

func NewHTTPClient(baseURL, token string, hc *http.Client) Client {
	return httpClient{wire.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c httpClient) Catalog(ctx context.Context, envelope identity.Envelope) ([]CatalogEntry, error) {
	var entries []CatalogEntry
	err := c.Do(ctx, http.MethodPost, "/v1/toolsurface/catalog", envelope, &entries)
	return entries, err
}

func Handler(server Client, token string) http.Handler {
	return wire.Handle(token, func(r *http.Request) (any, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/toolsurface/catalog" {
			return nil, ErrNotFound
		}
		var envelope identity.Envelope
		if err := wire.Decode(r, &envelope); err != nil {
			return nil, err
		}
		return NewInProc(server).Catalog(r.Context(), envelope)
	})
}
