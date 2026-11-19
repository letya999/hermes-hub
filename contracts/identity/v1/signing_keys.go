package identity

import (
	"context"
	"net/http"

	"github.com/letya999/hermes-hub/shared/httpapi"
)

func (c inProc) SigningKeys(ctx context.Context) ([]PublicSigningKey, error) {
	keys, err := c.server.SigningKeys(ctx)
	if err != nil {
		return nil, httpapi.Normalize(err)
	}
	if !validSigningKeys(keys) {
		return nil, ErrUnavailable
	}
	return httpapi.Copy(keys)
}

func (c httpClient) SigningKeys(ctx context.Context) ([]PublicSigningKey, error) {
	var keys []PublicSigningKey
	if err := c.Do(ctx, http.MethodGet, "/v1/identity/keys", nil, &keys); err != nil {
		return nil, err
	}
	if !validSigningKeys(keys) {
		return nil, ErrUnavailable
	}
	return keys, nil
}

func validSigningKeys(keys []PublicSigningKey) bool {
	seen := make(map[string]bool, len(keys))
	active := map[SigningPurpose]bool{}
	for _, key := range keys {
		if !key.Valid() || seen[key.ID] {
			return false
		}
		seen[key.ID] = true
		if key.VerifyUntil == 0 {
			if active[key.Purpose] {
				return false
			}
			active[key.Purpose] = true
		}
	}
	return len(active) == 4
}
