package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// runGovernance is the host-side tool-policy surface (issue 139). The agent
// has no write path into governance.json: rules and grants are authored here,
// behind --confirm, and the in-conversation grant_request +
// /grants/<id>?nonce= acknowledge pair activates a pending grant.
func runGovernance(_ context.Context, args []string) error {
	f := flag.NewFlagSet("governance", flag.ContinueOnError)
	kind := f.String("kind", "status", "status, rule, grant or revoke")
	user := f.String("user", "me", "principal a user-scope rule or grant targets")
	scope := f.String("scope", "user", "rule scope: global, org, user or default")
	subject := f.String("subject", "", "rule subject; defaults to --user for user scope")
	section := f.String("section", "", "inventoried section id (native:x, hub:x, user_mcp, org_mcp, toolhub)")
	match := f.String("match", "", "attribute selectors k=v,k=v for dynamic-tool rules")
	effect := f.String("effect", "allow", "rule effect: allow, deny or cap:read")
	expires := f.String("expires", "", "grant/rule expiry: duration (24h) or RFC3339")
	reason := f.String("reason", "", "audit reason, required for every mutation")
	issuer := f.String("issuer", "operator", "operator identity recorded as issuer and confirmer")
	revision := f.Uint64("revision", 1, "monotonic revision; increment when changing an existing record")
	grantID := f.String("grant", "", "grant id for revoke (default: derived)")
	confirm := f.Bool("confirm", false, "stamp this operator's confirmation and commit")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "ToolHub registry path")
	govPath := f.String("governance", os.Getenv("HUB_TOOL_GOVERNANCE"), "governance document path (default: beside the registry)")
	dir := f.String("dir", "", "private deployment directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *govPath == "" {
		if *storePath == "" {
			if *dir == "" {
				*dir = filepath.Join("spaces", *user)
			}
			absDir, err := filepath.Abs(*dir)
			if err != nil {
				return err
			}
			*storePath = filepath.Join(absDir, "runtime", "toolhub", "store.json")
		}
		*govPath = toolhub.GovernanceStorePath(*storePath)
	}
	if !filepath.IsAbs(*govPath) {
		abs, err := filepath.Abs(*govPath)
		if err != nil {
			return err
		}
		*govPath = abs
	}
	store := toolhub.NewGovernanceStore(*govPath)
	if store == nil {
		return fmt.Errorf("governance requires --governance or --toolhub-store")
	}
	now := time.Now().UTC()
	switch *kind {
	case "status":
		doc, err := store.Load()
		if err != nil {
			return err
		}
		grants := []map[string]any{}
		for _, g := range doc.Grants {
			state := g.Status
			if g.Status == toolhub.GrantActive && !now.Before(g.ExpiresAt) {
				state = "expired"
			}
			grants = append(grants, map[string]any{"grant_id": g.GrantID, "principal": g.PrincipalID, "section": g.Section, "status": state, "expires": g.ExpiresAt.Format(time.RFC3339), "reason": g.Reason})
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"path": *govPath, "revision": doc.Revision, "sections": len(doc.Inventory), "rules": len(doc.Rules), "grants": grants})
	case "rule":
		if *reason == "" || *section == "" && *match == "" || *section != "" && *match != "" {
			return fmt.Errorf("rule requires --reason and exactly one of --section/--match")
		}
		rule := toolhub.PolicyRule{Scope: *scope, Section: *section, Effect: *effect, Reason: *reason, GrantedBy: *issuer, Revision: *revision, Status: toolhub.ActiveStatus}
		rule.Subject = *subject
		if rule.Subject == "" && (*scope == toolhub.ScopeUser || *scope == toolhub.ScopeOrg) {
			rule.Subject = *user
		}
		if *match != "" {
			rule.Match = map[string]string{}
			for _, pair := range strings.Split(*match, ",") {
				k, v, ok := strings.Cut(pair, "=")
				if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
					return fmt.Errorf("--match entries are k=v")
				}
				rule.Match[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if *expires != "" {
			at, err := parseExpiry(*expires, now)
			if err != nil {
				return err
			}
			rule.ExpiresAt = at
		}
		rule.RuleID = deterministicRuleID(rule)
		if !*confirm {
			fmt.Fprintf(os.Stderr, "review digest: %s\nre-run with --confirm to commit\n", toolhub.ConfirmationDigest(rule))
			return fmt.Errorf("rule not confirmed")
		}
		updated := false
		err := store.Update(now, func(doc *toolhub.Governance) error {
			if err := doc.PutRule(rule, now); err != nil {
				return err
			}
			updated = true
			return nil
		})
		if err != nil || !updated {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"rule_id": rule.RuleID, "scope": rule.Scope, "subject": rule.Subject, "section": rule.Section, "effect": rule.Effect})
	case "grant":
		if *reason == "" || *section == "" || *expires == "" {
			return fmt.Errorf("grant requires --section, --expires and --reason")
		}
		at, err := parseExpiry(*expires, now)
		if err != nil {
			return err
		}
		grant := toolhub.NewToolGrant(deterministicGrantID(*user, *section, *grantID), *user, *section, *reason, *issuer, at, nil)
		if err := confirmRecord(*confirm, *issuer, &grant); err != nil {
			return err
		}
		if err := store.Update(now, func(doc *toolhub.Governance) error { return doc.PutToolGrant(grant, now) }); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"grant_id": grant.GrantID, "principal_id": grant.PrincipalID, "section": grant.Section, "status": grant.Status, "expires": grant.ExpiresAt.Format(time.RFC3339), "next_step": "the principal activates it in-conversation: grant_request then the acknowledge URL"})
	case "revoke":
		if *grantID == "" || *reason == "" {
			return fmt.Errorf("revoke requires --grant and --reason")
		}
		if err := store.Update(now, func(doc *toolhub.Governance) error { return doc.RevokeToolGrant(*grantID, *issuer, *reason, now) }); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"grant_id": *grantID, "status": toolhub.GrantRevoked})
	default:
		return fmt.Errorf("unknown --kind %q", *kind)
	}
}

func parseExpiry(raw string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("expiry must be in the future")
		}
		return now.Add(d).UTC(), nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil || !at.After(now) {
		return time.Time{}, fmt.Errorf("invalid --expires: duration like 24h or a future RFC3339 time")
	}
	return at.UTC(), nil
}

func deterministicRuleID(r toolhub.PolicyRule) string {
	return governanceID("govrule", r.Scope, r.Subject, r.Section, r.Effect)
}

func deterministicGrantID(user, section, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return governanceID("toolgrant", user, section, time.Now().UTC().Format("20060102150405"))
}

func governanceID(prefix string, values ...string) string {
	joined := strings.Join(append([]string{prefix}, values...), "-")
	var b strings.Builder
	for _, c := range strings.ToLower(joined) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		} else if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}
