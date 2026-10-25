package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/dns"
	"github.com/letya999/hermes-hub/internal/exposure"
	"github.com/letya999/hermes-hub/internal/sshcap"
)

// runDNS implements `hubctl dns <check|ensure|remove|records|cert|push|delegate-check>` — the operator surface
// of the per-user addressing layer (SPEC-0076). DNS credentials resolve
// through Resolver per call; nothing secret reaches stdout.
func runDNS(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("dns", flag.ContinueOnError)
	dir := f.String("dir", "", "private deployment directory")
	ip := f.String("ip", "", "A record address")
	endpoint := f.String("endpoint", "", "provider API base URL override")
	acmeDir := f.String("acme", "", "ACME directory URL")
	alias := f.String("alias", "", "sshcap host alias for push")
	certDir := f.String("cert-dir", "/etc/sish/certs", "sish --https-certificate-directory")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	cfgPath := filepath.Join(*dir, "connections", "dns", "config.yaml")
	cfg, err := dns.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("%s: %w — regru requires a statically allowlisted egress IP (set static_egress_ip), or delegate hub.<domain> to another provider", cfgPath, err)
	}
	switch args[0] {
	case "check":
		fmt.Printf("dns profile: %s zone=%s base=%s\n", cfg.Provider, cfg.Zone, cfg.Base)
		return nil
	case "ensure":
		user := f.Arg(0)
		if user == "" || *ip == "" {
			return fmt.Errorf("usage: hubctl dns ensure --ip <addr> [--dir d] <user>")
		}
		if err := boundZone(*dir, user); err != nil {
			return err
		}
		cfgDir := filepath.Dir(cfgPath)
		p, release, err := dnsProvider(ctx, cfg, cfgDir, *endpoint)
		if err != nil {
			return err
		}
		defer release()
		rec := dns.Record{Name: "*." + user, Type: "A", Data: *ip, TTL: 300}
		out, err := p.Ensure(ctx, rec)
		if err != nil {
			return err
		}
		ledger, err := audit.Open(filepath.Join(*dir, "state", "audit.jsonl"))
		if err == nil {
			ev := audit.NewEvent("dns", "operator", "ensure")
			ev.Name = cfg.FQDN(rec.Name)
			_ = ledger.Append(ev)
		}
		fmt.Printf("ensured %s A %s (ttl %d)\n", cfg.FQDN(out.Record.Name), out.Record.Data, out.EffectiveTTL)
		return nil
	case "cert":
		user := f.Arg(0)
		if user == "" {
			return fmt.Errorf("usage: hubctl dns cert --acme <dir-url> [--dir d] <user>")
		}
		if err := boundZone(*dir, user); err != nil {
			return err
		}
		cfgDir := filepath.Dir(cfgPath)
		store := &dns.Store{Dir: filepath.Join(cfgDir, "certs")}
		if expiry, err := store.Expiry(user); err == nil && time.Until(expiry) > 30*24*time.Hour {
			fmt.Printf("cert for %s valid until %s — renewal due inside 30 days\n", user, expiry.Format("2006-01-02"))
			return nil
		}
		if *acmeDir == "" {
			return fmt.Errorf("usage: hubctl dns cert --acme <dir-url> [--dir d] <user>")
		}
		p, release, err := dnsProvider(ctx, cfg, cfgDir, *endpoint)
		if err != nil {
			return err
		}
		defer release()
		acctKey, err := acmeAccountKey(cfgDir)
		if err != nil {
			return err
		}
		cert, key, err := (&dns.Issuer{Dir: *acmeDir, Key: acctKey, Provider: p, Config: cfg}).IssueWildcard(ctx, user)
		if err != nil {
			// R7: if a live cert exists this was a renewal — alert in the
			// ledger; stored material stays untouched.
			if _, _, serr := store.Load(user); serr == nil {
				if ledger, lerr := audit.Open(filepath.Join(*dir, "state", "audit.jsonl")); lerr == nil {
					ev := audit.NewEvent("dns", "operator", "renew-failed")
					ev.Name = "*." + user + "." + cfg.Base
					_ = ledger.Append(ev)
				}
			}
			return err
		}
		if err := store.Save(user, cert, key); err != nil {
			return err
		}
		if ledger, lerr := audit.Open(filepath.Join(*dir, "state", "audit.jsonl")); lerr == nil {
			ev := audit.NewEvent("dns", "operator", "cert")
			ev.Name = "*." + user + "." + cfg.Base
			_ = ledger.Append(ev)
		}
		fmt.Printf("issued *.%s.%s → %s\n", user, cfg.Base, store.Dir)
		return nil
	case "push":
		if err := boundZone(*dir, f.Arg(0)); err != nil {
			return err
		}
		cert, key, err := (&dns.Store{Dir: filepath.Join(filepath.Dir(cfgPath), "certs")}).Load(f.Arg(0))
		if err != nil {
			return err
		}
		svc, err := sshcap.Open(sshcap.Options{
			ConfigPath: filepath.Join(*dir, "connections", "ssh", "config.yaml"),
			Grants:     sshcap.Grants{Write: true},
			AuditPath:  filepath.Join(*dir, "state", "audit.jsonl"),
		})
		if err != nil {
			return err
		}
		defer svc.Close()
		w := func(ctx context.Context, host, path, text string) error {
			_, err := svc.Write(ctx, host, path, text)
			return err
		}
		if err := dns.PushCert(ctx, dns.RemoteWriter(w), *alias, *certDir, f.Arg(0), cert, key); err != nil {
			return err
		}
		fmt.Printf("pushed %s cert to %s:%s\n", f.Arg(0), *alias, *certDir)
		return nil
	case "remove":
		user := f.Arg(0)
		if user == "" || *ip == "" {
			return fmt.Errorf("usage: hubctl dns remove --ip <addr> [--dir d] <user>")
		}
		if err := boundZone(*dir, user); err != nil {
			return err
		}
		p, release, err := dnsProvider(ctx, cfg, filepath.Dir(cfgPath), *endpoint)
		if err != nil {
			return err
		}
		defer release()
		rec := dns.Record{Name: "*." + user, Type: "A", Data: *ip}
		if err := p.Remove(ctx, rec); err != nil {
			return err
		}
		if ledger, lerr := audit.Open(filepath.Join(*dir, "state", "audit.jsonl")); lerr == nil {
			ev := audit.NewEvent("dns", "operator", "remove")
			ev.Name = cfg.FQDN(rec.Name)
			_ = ledger.Append(ev)
		}
		fmt.Printf("removed %s\n", cfg.FQDN(rec.Name))
		return nil
	case "delegate-check":
		if err := cfg.CheckDelegation(ctx); err != nil {
			return err
		}
		fmt.Printf("%s delegated to %s\n", cfg.Base, strings.Join(cfg.NameServers, ","))
		return nil
	case "records":
		p, release, err := dnsProvider(ctx, cfg, filepath.Dir(cfgPath), *endpoint)
		if err != nil {
			return err
		}
		defer release()
		recs, err := p.List(ctx)
		if err != nil {
			return err
		}
		for _, r := range recs {
			fmt.Printf("%s %s %s ttl=%d\n", r.Name, r.Type, r.Data, r.TTL)
		}
		return nil
	}
	return fmt.Errorf("unknown dns subcommand %q", args[0])
}

// boundZone enforces SPEC-0076 R4: <user> must equal the zone label bound in
// the space's reviewed exposure config. Missing config = unbound = denied.
func boundZone(dir, user string) error {
	ecfg, err := exposure.Load(filepath.Join(dir, "connections", "exposure", "config.yaml"))
	if err != nil {
		return fmt.Errorf("%w: user zone %q is not bound — exposure config: %v", dns.ErrDenied, user, err)
	}
	if ecfg.Zone != user {
		if ledger, lerr := audit.Open(filepath.Join(dir, "state", "audit.jsonl")); lerr == nil {
			ev := audit.NewEvent("dns", "operator", "denied")
			ev.Name = user
			_ = ledger.Append(ev)
		}
		return fmt.Errorf("%w: zone label %q is bound to this space, not %q", dns.ErrDenied, ecfg.Zone, user)
	}
	return nil
}

// dnsProvider resolves the profile credential and builds the record client.
func dnsProvider(ctx context.Context, cfg *dns.Config, cfgDir, endpoint string) (dns.Provider, func(), error) {
	res := &dns.Resolver{Root: os.DirFS(cfgDir)}
	creds, release, err := res.Resolve(ctx, cfg.CredentialRef)
	if err != nil {
		return nil, nil, err
	}
	p, err := cfg.NewProvider(creds, dns.Options{BaseURL: endpoint})
	if err != nil {
		release()
		return nil, nil, err
	}
	return p, release, nil
}

// acmeAccountKey loads the operator's ACME account key from dir, generating
// and persisting it (0600) on first use — one account identity per space.
func acmeAccountKey(dir string) (*ecdsa.PrivateKey, error) {
	path := filepath.Join(dir, "acme-account.key")
	if raw, err := os.ReadFile(path); err == nil { // #nosec G304 -- operator-owned path
		block, _ := pem.Decode(raw)
		if block != nil {
			return x509.ParseECPrivateKey(block.Bytes)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}
