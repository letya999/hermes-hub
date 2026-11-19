package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	primitive "github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

const (
	ModelServiceTokenHeader = "X-Hub-Model-Service-Token"
	PersonalModelAudience   = "model-gateway-personal-supervisor"
	PersonalModelPrincipal  = "system:hub-control"
	DreamingModelAudience   = "model-gateway-dreaming"
	DreamingModelPrincipal  = "system:toolhub"
	DreamingServiceAuthEnv  = "HUB_DREAMING_SERVICE_AUTH"
	DreamingModelTokenPath  = "/v1/identity/dreaming-model-token"
	ServicePassportsPath    = "/v1/identity/service-passports"
)

type DreamingModelSigner interface {
	IssueDreamingModelService(context.Context, ServiceRequest) (string, error)
}

func NewDreamingModelSigner(baseURL, bootstrap string) DreamingModelSigner {
	return newServiceTokenHTTP(baseURL, bootstrap)
}

func (c serviceTokenHTTP) IssueDreamingModelService(ctx context.Context, request ServiceRequest) (string, error) {
	return c.issue(ctx, DreamingModelTokenPath, request)
}

func DreamingModelTokenHandler(signer DreamingModelSigner, bootstrap, environment string) http.Handler {
	return serviceTokenHandler(signer.IssueDreamingModelService, bootstrap, environment)
}

func VerifyModelServiceToken(token string, request ServiceRequest, keys []PublicSigningKey, now time.Time) (string, error) {
	claims, err := verifyServiceRequest(token, request, keys, now)
	if err != nil || !ValidPolicyDigest(request.PolicyRevision) {
		return "", ErrForbidden
	}
	switch claims.Audience {
	case PersonalModelAudience:
		if claims.Service == PersonalModelPrincipal {
			return claims.Audience, nil
		}
	case DreamingModelAudience:
		if claims.Service == DreamingModelPrincipal {
			return claims.Audience, nil
		}
	}
	return "", ErrForbidden
}

func ModelServiceRight(audience string) (capability, operation, scope string) {
	switch audience {
	case PersonalModelAudience:
		return "agents", "monitor_self", "runtime"
	case DreamingModelAudience:
		return "memory", "dreaming", ""
	default:
		return "", "", ""
	}
}

func (b PolicyBundle) AllowsModelService(audience string) bool {
	capability, operation, _ := ModelServiceRight(audience)
	if b.Identity != nil || !b.HasPassportRight(capability, operation) || !b.HasPassportRight("models", "use") {
		return false
	}
	if audience == DreamingModelAudience {
		var config struct {
			Memory struct {
				Enabled bool `json:"enabled"`
			} `json:"memory"`
		}
		return json.Unmarshal(b.Passports["config"], &config) == nil && config.Memory.Enabled
	}
	return true
}

type ServicePassportsReader interface {
	ActiveServicePassports(ctx context.Context, space, environment string) (string, error)
}

type servicePassportsHTTP struct{ httpapi.Client }

func NewServicePassportsReader(baseURL, token string, hc *http.Client) ServicePassportsReader {
	return servicePassportsHTTP{httpapi.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
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
	return httpapi.Handle(token, func(r *http.Request) (any, error) {
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
