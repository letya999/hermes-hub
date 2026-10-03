package mediasvc

// broker.go wires provider credentials through Credential Broker: when
// HUB_MEDIA_BROKER_GRANT names a grant, every engine call acquires a short
// lease, materializes the credential and releases — the plaintext key lives
// only in process memory, never in env files, the job store or logs.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
)

const (
	mediaBrokerBinding  = "hub-media"
	mediaBrokerWorkload = "hub-media"
	// mediaBrokerCacheTTL bounds how long a materialized key stays in memory
	// before the next engine call re-materializes — a fal video job polls every
	// few seconds and must not turn each poll into a lease round-trip.
	mediaBrokerCacheTTL = 5 * time.Minute
)

// mediaBrokerEnvNames is the materialized-env lookup order. A media provider
// contract should deliver one of these targets; HUB_MEDIA_UPSTREAM_KEY is the
// canonical name.
var mediaBrokerEnvNames = []string{"HUB_MEDIA_UPSTREAM_KEY", "OPENAI_API_KEY", "FAL_KEY", "MEDIA_API_KEY"}

// keyResolver resolves the provider credential for one engine call.
type keyResolver = func(ctx context.Context) (string, error)

// staticKey is the env-file path used when no broker grant is configured; an
// empty value means the engine sends no credential (public upstream) or fails
// at config validation (fal).
func staticKey(value string) keyResolver {
	return func(context.Context) (string, error) { return value, nil }
}

// mediaKeyResolver selects the credential source: a broker grant wins over
// static env. A grant without broker client env is a config error — better a
// service that refuses to boot than one silently leaking into env mode.
func (c Config) mediaKeyResolver() (keyResolver, error) {
	if c.BrokerGrant == "" {
		return nil, nil
	}
	if !c.Broker.Enabled() {
		return nil, errors.New("HUB_MEDIA_BROKER_GRANT requires HUB_CREDENTIAL_BROKER_MEDIA_URL, key file, key id and issuer")
	}
	auth := c.brokerIdentity()
	resolve := func(ctx context.Context) (string, error) {
		return brokerMaterializeKey(ctx, c.Broker, auth, c.BrokerGrant)
	}
	return cachedKey(resolve, mediaBrokerCacheTTL), nil
}

func (c Config) brokerIdentity() identity.Envelope {
	return identity.Envelope{
		Schema:             identity.Schema,
		PrincipalID:        c.PrincipalID,
		ExternalIdentityID: c.PrincipalID,
		ContextID:          c.ContextID,
		RuntimeID:          c.RuntimeID,
		ConversationID:     mediaBrokerWorkload,
		DeliveryTargetID:   mediaBrokerWorkload,
		PolicyVersion:      c.PolicyVersion,
	}
}

// brokerMaterializeKey runs acquire → materialize → release against the
// broker for the media grant. The lease is released before the provider call;
// the plaintext is cleared from the DTO right after extraction.
func brokerMaterializeKey(ctx context.Context, cfg credentialbroker.Config, auth identity.Envelope, grantID string) (string, error) {
	control, err := cfg.NewForBinding(auth, "broker:control", mediaBrokerBinding, mediaBrokerWorkload)
	if err != nil {
		return "", fmt.Errorf("media broker control client: %w", err)
	}
	runtimeClient, err := cfg.NewForBinding(auth, "broker:runtime", mediaBrokerBinding, mediaBrokerWorkload)
	if err != nil {
		return "", fmt.Errorf("media broker runtime client: %w", err)
	}
	lease, err := control.Acquire(ctx, brokerv1.AcquireLease{GrantID: grantID, TTLSeconds: 120})
	if err != nil {
		return "", fmt.Errorf("media credential acquire: %w", err)
	}
	materialized, err := runtimeClient.Materialize(ctx, lease.ID)
	release := func() {
		_ = runtimeClient.Release(context.Background(), lease.ID, brokerv1.RuntimeRelease{})
	}
	if err != nil {
		release()
		return "", fmt.Errorf("media credential materialize: %w", err)
	}
	value := ""
	for _, name := range mediaBrokerEnvNames {
		if v := materialized.Env[name]; v != "" {
			value = v
			break
		}
	}
	clear(materialized.Env)
	release()
	if value == "" {
		return "", errors.New("media credential delivered no provider key")
	}
	return value, nil
}

// cachedKey memoizes a resolver for ttl; errors are never cached.
func cachedKey(fn keyResolver, ttl time.Duration) keyResolver {
	var mu sync.Mutex
	var key string
	var at time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if key != "" && time.Since(at) < ttl {
			return key, nil
		}
		value, err := fn(ctx)
		if err != nil {
			return "", err
		}
		key, at = value, time.Now()
		return key, nil
	}
}
