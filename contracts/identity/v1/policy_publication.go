package identity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	primitive "github.com/letya999/hermes-hub/internal/identity"
)

const PassportPublicationPath = "/v1/admin/identity/passports"

// PassportPublisher is the distinct control-admin authority for publication.
type PassportPublisher interface {
	StagePassports(context.Context, PolicyBundle) (SignedPolicy, error)
	ActivePassports(ctx context.Context, space, environment string) (string, error)
	ActivatePassports(ctx context.Context, space, environment, previous, next string) error
}

type passportActivation struct {
	Previous string `json:"previous"`
	Next     string `json:"next"`
}

type passportPointer struct {
	Digest string `json:"digest"`
}

type passportPublisherHTTP struct{ wire.Client }

func NewPassportPublisherHTTPClient(baseURL, token string, hc *http.Client) PassportPublisher {
	return passportPublisherHTTP{wire.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c passportPublisherHTTP) StagePassports(ctx context.Context, bundle PolicyBundle) (SignedPolicy, error) {
	_, digest, err := bundle.Canonical()
	if err != nil || bundle.Identity != nil {
		return SignedPolicy{}, ErrInvalid
	}
	var result SignedPolicy
	if err := c.Do(ctx, http.MethodPost, PassportPublicationPath, bundle, &result); err != nil {
		return SignedPolicy{}, err
	}
	return checkPolicyResponse(result, digest)
}

func passportPublicationPath(space, environment string) string {
	query := url.Values{"space": {space}, "environment": {environment}}
	return PassportPublicationPath + "?" + query.Encode()
}

func (c passportPublisherHTTP) ActivePassports(ctx context.Context, space, environment string) (string, error) {
	var result passportPointer
	if err := c.Do(ctx, http.MethodGet, passportPublicationPath(space, environment), nil, &result); err != nil {
		return "", err
	}
	if !ValidPolicyDigest(result.Digest) {
		return "", ErrUnavailable
	}
	return result.Digest, nil
}

func (c passportPublisherHTTP) ActivatePassports(ctx context.Context, space, environment, previous, next string) error {
	var result passportPointer
	if err := c.Do(ctx, http.MethodPut, passportPublicationPath(space, environment), passportActivation{Previous: previous, Next: next}, &result); err != nil {
		return err
	}
	if result.Digest != next {
		return ErrUnavailable
	}
	return nil
}

func PassportPublicationHandler(publisher PassportPublisher, token, environment string, validate func(PolicyBundle) error) http.Handler {
	return wire.Handle(token, func(r *http.Request) (any, error) {
		if r.Header.Get("Origin") != "" {
			return nil, ErrForbidden
		}
		decode := func(out any) bool {
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			return d.Decode(out) == nil && d.Decode(new(any)) == io.EOF
		}
		if r.Method == http.MethodPost {
			var bundle PolicyBundle
			if !decode(&bundle) || bundle.Environment != environment || bundle.Identity != nil ||
				!primitive.ValidID(bundle.Space) || validate(bundle) != nil {
				return nil, ErrInvalid
			}
			return publisher.StagePassports(r.Context(), bundle)
		}
		space := r.URL.Query().Get("space")
		if !primitive.ValidID(space) || r.URL.Query().Get("environment") != environment {
			return nil, ErrInvalid
		}
		switch r.Method {
		case http.MethodGet:
			digest, err := publisher.ActivePassports(r.Context(), space, environment)
			return passportPointer{Digest: digest}, err
		case http.MethodPut:
			var update passportActivation
			if !decode(&update) || (update.Previous != "" && !ValidPolicyDigest(update.Previous)) ||
				(update.Next != "" && !ValidPolicyDigest(update.Next)) {
				return nil, ErrInvalid
			}
			return passportPointer{Digest: update.Next}, publisher.ActivatePassports(r.Context(), space, environment, update.Previous, update.Next)
		default:
			return nil, ErrInvalid
		}
	})
}
