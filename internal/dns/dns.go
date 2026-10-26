// Package dns implements the provider-neutral record contract for the
// per-user addressing layer (ADR-0080/SPEC-0078). Control-plane only.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid dns config")
	ErrUnavailable = errors.New("dns provider unavailable")
	ErrDenied      = errors.New("dns operation denied")
	ErrNotFound    = errors.New("dns record not found")
	ErrAuth        = errors.New("dns provider authentication failed")
	ErrQuota       = errors.New("dns provider rate limit")
	ErrPropagation = errors.New("dns record not visible at authoritative NS")
)

type Record struct {
	Name string
	Type string
	Data string
	TTL  int
}

type Result struct {
	Record       Record
	EffectiveTTL int
}

type Provider interface {
	Ensure(ctx context.Context, rec Record) (Result, error)
	Remove(ctx context.Context, rec Record) error
	List(ctx context.Context) ([]Record, error)
}

// Lookuper answers a query against one authoritative nameserver.
type Lookuper func(ctx context.Context, server, fqdn, typ string) ([]string, error)

// WaitAuthoritative polls the serving nameservers until any returns want
// among the answers for fqdn/typ, or the deadline lapses. Fail-fast
// pre-check — resolver propagation by TTL is out of scope by contract.
func WaitAuthoritative(ctx context.Context, fqdn, typ, want string, ns []string, deadline time.Duration, lookup Lookuper) error {
	if len(ns) == 0 {
		return fmt.Errorf("%w: no authoritative nameservers configured", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		for _, server := range ns {
			got, err := lookup(ctx, server, fqdn, typ)
			if err != nil {
				continue
			}
			for _, answer := range got {
				if equalRData(answer, want) {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s %s at %s", ErrPropagation, fqdn, typ, strings.Join(ns, ","))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func equalRData(got, want string) bool {
	return strings.EqualFold(strings.TrimSuffix(got, "."), strings.TrimSuffix(want, "."))
}

// LookupNS resolves the nameservers serving a zone — stubbed in tests,
// net.LookupNS in production.
var LookupNS = func(ctx context.Context, zone string) ([]string, error) {
	ns, err := net.DefaultResolver.LookupNS(ctx, zone)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Host)
	}
	return out, nil
}

// CheckDelegation verifies every configured name server serves Base.
func (c *Config) CheckDelegation(ctx context.Context) error {
	live, err := LookupNS(ctx, c.Base)
	if err != nil {
		return fmt.Errorf("%w: ns lookup %s: %v", ErrPropagation, c.Base, err)
	}
	seen := map[string]bool{}
	for _, h := range live {
		seen[strings.TrimSuffix(strings.ToLower(h), ".")] = true
	}
	var missing []string
	for _, want := range c.NameServers {
		if !seen[strings.ToLower(strings.TrimSuffix(want, "."))] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s not delegated to %s", ErrPropagation, c.Base, strings.Join(missing, ","))
	}
	return nil
}
