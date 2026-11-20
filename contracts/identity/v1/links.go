package identity

import (
	"context"
	"net/http"
	"strings"

	primitive "github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// OIDCLink is accepted only from authenticated channel ingress after its
// nonce/PKCE/code/ID-token checks. It is never an agent tool or a read method.
type OIDCLink struct {
	Channel     ChannelIdentity `json:"channel"`
	PrincipalID string          `json:"principal_id,omitempty"`
	Issuer      string          `json:"issuer"`
	Subject     string          `json:"subject"`
}

type UnlinkRequest struct {
	Channel     ChannelIdentity `json:"channel"`
	PrincipalID string          `json:"principal_id"`
}

type LinkClient interface {
	VerifyOIDC(context.Context, OIDCLink) error
	Unlink(context.Context, UnlinkRequest) error
}

func validLink(value OIDCLink) bool {
	return (value.Channel.Channel == Telegram || value.Channel.Channel == Slack) && value.Channel.ID != "" && len(value.Channel.ID) <= 256 && !strings.ContainsAny(value.Channel.ID, "\r\n\t ") && (value.PrincipalID == "" || validID(value.PrincipalID)) && value.Issuer != "" && len(value.Issuer) <= primitive.MaxProvenanceBytes && value.Subject != "" && len(value.Subject) <= primitive.MaxProvenanceBytes && !strings.ContainsAny(value.Issuer+value.Subject, "\r\n")
}

type linkHTTPClient struct{ httpapi.Client }

func NewLinkHTTPClient(baseURL, token string, hc *http.Client) LinkClient {
	return linkHTTPClient{httpapi.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}
func (c linkHTTPClient) VerifyOIDC(ctx context.Context, value OIDCLink) error {
	if !validLink(value) {
		return ErrInvalid
	}
	var out struct{}
	return c.Do(ctx, http.MethodPost, "/v1/identity/links/oidc", value, &out)
}
func (c linkHTTPClient) Unlink(ctx context.Context, value UnlinkRequest) error {
	if !validID(value.PrincipalID) {
		return ErrInvalid
	}
	var out struct{}
	return c.Do(ctx, http.MethodPost, "/v1/identity/links/revoke", value, &out)
}

// LinkHandler has a separate ingress credential. The read/service-control
// credential and browser-origin requests cannot assert OIDC proof.
func LinkHandler(server LinkClient, token string) http.Handler {
	return httpapi.Handle(token, func(r *http.Request) (any, error) {
		if r.Header.Get("Origin") != "" {
			return nil, ErrForbidden
		}
		if r.Method != http.MethodPost {
			return nil, ErrNotFound
		}
		switch r.URL.Path {
		case "/v1/identity/links/oidc":
			var value OIDCLink
			if err := httpapi.Decode(r, &value); err != nil {
				return nil, err
			}
			if !validLink(value) {
				return nil, ErrInvalid
			}
			if err := server.VerifyOIDC(r.Context(), value); err != nil {
				return nil, httpapi.Normalize(err)
			}
		case "/v1/identity/links/revoke":
			var value UnlinkRequest
			if err := httpapi.Decode(r, &value); err != nil {
				return nil, err
			}
			if !validID(value.PrincipalID) {
				return nil, ErrInvalid
			}
			if err := server.Unlink(r.Context(), value); err != nil {
				return nil, httpapi.Normalize(err)
			}
		default:
			return nil, ErrNotFound
		}
		return struct{}{}, nil
	})
}
