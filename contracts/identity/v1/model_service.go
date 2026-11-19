package identity

import (
	"context"
	"net/http"
	"net/url"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	primitive "github.com/letya999/hermes-hub/internal/identity"
)

const (
	ModelServiceTokenHeader = "X-Hub-Model-Service-Token"
	PersonalModelAudience   = "model-gateway-personal-supervisor"
	PersonalModelPrincipal  = "system:hub-control"
	ServicePassportsPath    = "/v1/identity/service-passports"
)

type ServicePassportsReader interface {
	ActiveServicePassports(ctx context.Context, space, environment string) (string, error)
}

type servicePassportsHTTP struct{ wire.Client }

func NewServicePassportsReader(baseURL, token string, hc *http.Client) ServicePassportsReader {
	return servicePassportsHTTP{wire.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c servicePassportsHTTP) ActiveServicePassports(ctx context.Context, space, environment string) (string, error) {
	if !primitive.ValidID(space) || (environment != "dev" && environment != "prod") {
		return "", ErrInvalid
	}
	query := url.Values{"space": {space}, "environment": {environment}}
	var result passportPointer
	if err := c.Do(ctx, http.MethodGet, ServicePassportsPath+"?"+query.Encode(), nil, &result); err != nil {
		return "", err
	}
	if !ValidPolicyDigest(result.Digest) {
		return "", ErrUnavailable
	}
	return result.Digest, nil
}

func ServicePassportsHandler(reader ServicePassportsReader, token, environment string) http.Handler {
	return wire.Handle(token, func(r *http.Request) (any, error) {
		if r.Method != http.MethodGet || r.Header.Get("Origin") != "" {
			return nil, ErrForbidden
		}
		space := r.URL.Query().Get("space")
		if !primitive.ValidID(space) || r.URL.Query().Get("environment") != environment {
			return nil, ErrInvalid
		}
		digest, err := reader.ActiveServicePassports(r.Context(), space, environment)
		return passportPointer{Digest: digest}, err
	})
}
