package dns

import (
	"net/http"
	"time"
)

// Options tunes provider construction; BaseURL overrides the API endpoint
// in tests. Timeout caps each provider HTTP call (SPEC-0078 R8 bounds).
type Options struct {
	HTTPClient *http.Client
	BaseURL    string
	Timeout    time.Duration
}

func (o Options) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: o.Timeout}
}

// NewProvider builds the record client for the configured profile from the
// materialized credential map.
func (c *Config) NewProvider(creds map[string]string, opts Options) (Provider, error) {
	switch c.Provider {
	case "regru":
		return newRegru(c, creds, opts)
	case "digitalocean":
		return newDigitalOcean(c, creds, opts)
	case "gcloud":
		return newGcloud(c, creds, opts)
	default:
		return nil, ErrInvalid
	}
}
