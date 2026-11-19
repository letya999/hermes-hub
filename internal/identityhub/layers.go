package identityhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

var modeOrder = map[string]int{"read-only": 0, "plan": 1, "ask": 2, "ask-critical": 3, "full-access": 4}

func normalized(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string{}, values...)
	slices.Sort(result)
	return slices.Compact(result)
}

func normalizeRestrictions(value contract.Restrictions) (contract.Restrictions, error) {
	for _, list := range [][]string{value.Grants, value.Skills, value.Guardrails, value.MemoryScopes} {
		if len(list) > 256 {
			return contract.Restrictions{}, contract.ErrInvalid
		}
		for _, item := range list {
			if item == "" || len(item) > 128 || strings.TrimSpace(item) != item || strings.ContainsAny(item, "\r\n\x00") {
				return contract.Restrictions{}, contract.ErrInvalid
			}
		}
	}
	if _, known := modeOrder[value.Mode]; value.Mode != "" && !known {
		return contract.Restrictions{}, contract.ErrInvalid
	}
	value.Grants = normalized(value.Grants)
	value.Skills = normalized(value.Skills)
	value.Guardrails = normalized(value.Guardrails)
	value.MemoryScopes = normalized(value.MemoryScopes)
	return value, nil
}

func intersection(parent, child []string) []string {
	if parent == nil {
		return normalized(child)
	}
	if child == nil {
		return normalized(parent)
	}
	result := []string{}
	for _, item := range child {
		if slices.Contains(parent, item) {
			result = append(result, item)
		}
	}
	return normalized(result)
}

func compose(parent, child contract.Restrictions) contract.Restrictions {
	result := contract.Restrictions{
		Grants: intersection(parent.Grants, child.Grants), Skills: intersection(parent.Skills, child.Skills), MemoryScopes: intersection(parent.MemoryScopes, child.MemoryScopes),
		Guardrails: normalized(append(slices.Clone(parent.Guardrails), child.Guardrails...)), Mode: parent.Mode,
	}
	if child.Mode != "" && (result.Mode == "" || modeOrder[child.Mode] < modeOrder[result.Mode]) {
		result.Mode = child.Mode
	}
	return result
}

func checkNarrowing(parent, child contract.Restrictions) error {
	for _, pair := range [][2][]string{{parent.Grants, child.Grants}, {parent.Skills, child.Skills}, {parent.MemoryScopes, child.MemoryScopes}} {
		if pair[0] == nil || pair[1] == nil {
			continue
		}
		for _, item := range pair[1] {
			if !slices.Contains(pair[0], item) {
				return contract.ErrForbidden
			}
		}
	}
	if parent.Mode != "" && child.Mode != "" && modeOrder[child.Mode] > modeOrder[parent.Mode] {
		return contract.ErrForbidden
	}
	return nil
}

func readLayer(ctx context.Context, tx *sql.Tx, scope string) (contract.Restrictions, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT restrictions FROM layers WHERE scope=?", scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.Restrictions{}, nil
	}
	if err != nil {
		return contract.Restrictions{}, storeError(err)
	}
	var result contract.Restrictions
	if json.Unmarshal(raw, &result) != nil {
		return result, contract.ErrUnavailable
	}
	normalized, err := normalizeRestrictions(result)
	if err != nil {
		return contract.Restrictions{}, contract.ErrUnavailable
	}
	return normalized, nil
}

func inheritedLayers(ctx context.Context, tx *sql.Tx, scopes ...string) (contract.Restrictions, error) {
	var result contract.Restrictions
	for _, scope := range scopes {
		if scope == "" {
			continue
		}
		layer, err := readLayer(ctx, tx, scope)
		if err != nil {
			return contract.Restrictions{}, err
		}
		result = compose(result, layer)
	}
	return result, nil
}

func orgScope(org string) string {
	if org == "" {
		return ""
	}
	return "org:" + org
}

func (r *Registry) SetLayer(ctx context.Context, actor, scope string, restrictions contract.Restrictions) error {
	value, err := normalizeRestrictions(restrictions)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var parent contract.Restrictions
	switch {
	case scope == "platform":
		if err := r.authorizeAdmin(ctx, tx, actor, scope, "layers.write"); err != nil {
			return err
		}
	case strings.HasPrefix(scope, "org:") && validAdminScope(scope):
		if err := r.authorizeAdmin(ctx, tx, actor, scope, "layers.write"); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM organizations WHERE id=?", strings.TrimPrefix(scope, "org:")).Scan(&count); err != nil {
			return storeError(err)
		}
		if count != 1 {
			return contract.ErrNotFound
		}
		parent, err = inheritedLayers(ctx, tx, "platform")
	case strings.HasPrefix(scope, "user:") && identity.ValidID(strings.TrimPrefix(scope, "user:")):
		owner := strings.TrimPrefix(scope, "user:")
		var org string
		if err := tx.QueryRowContext(ctx, "SELECT org_id FROM principals WHERE id=? AND kind='human' AND enabled=1", owner).Scan(&org); err != nil {
			return contract.ErrForbidden
		}
		if actor != owner {
			adminScope := orgScope(org)
			if adminScope == "" {
				adminScope = "platform"
			}
			if err := r.authorizeAdmin(ctx, tx, actor, adminScope, "layers.write"); err != nil {
				return err
			}
		}
		parent, err = inheritedLayers(ctx, tx, "platform", orgScope(org))
	default:
		return contract.ErrInvalid
	}
	if err != nil {
		return err
	}
	if err := checkNarrowing(parent, value); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return contract.ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO layers(scope,restrictions) VALUES(?,?) ON CONFLICT(scope) DO UPDATE SET restrictions=excluded.restrictions", scope, raw); err != nil {
		return storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "layer_set", actor, scope, "policy_narrowing"); err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

func (r *Registry) PutAgentProfile(ctx context.Context, owner, name string, restrictions contract.Restrictions) (contract.Principal, error) {
	if !identity.ValidID(name) {
		return contract.Principal{}, contract.ErrInvalid
	}
	value, err := normalizeRestrictions(restrictions)
	if err != nil {
		return contract.Principal{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return contract.Principal{}, storeError(err)
	}
	defer tx.Rollback()
	var org string
	if err := tx.QueryRowContext(ctx, "SELECT org_id FROM principals WHERE id=? AND kind='human' AND enabled=1", owner).Scan(&org); err != nil {
		return contract.Principal{}, contract.ErrForbidden
	}
	parent, err := inheritedLayers(ctx, tx, "platform", orgScope(org), "user:"+owner)
	if err != nil {
		return contract.Principal{}, err
	}
	if err := checkNarrowing(parent, value); err != nil {
		return contract.Principal{}, err
	}
	principal := contract.Principal{ID: "agent:" + owner + ":" + name, Kind: contract.Agent, OwnerID: owner, OrganizationID: org}
	var existingOwner, existingKind string
	err = tx.QueryRowContext(ctx, "SELECT coalesce(owner_id,''),kind FROM principals WHERE id=?", principal.ID).Scan(&existingOwner, &existingKind)
	if err == nil {
		if existingOwner != owner || existingKind != "agent" {
			return contract.Principal{}, contract.ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,owner_id,org_id,enabled) VALUES(?,'agent',?,?,1)", principal.ID, owner, org); err != nil {
			return contract.Principal{}, storeError(err)
		}
	} else {
		return contract.Principal{}, storeError(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return contract.Principal{}, contract.ErrInvalid
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO profiles(id,name,space,restrictions) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET restrictions=excluded.restrictions", principal.ID, name, owner, raw); err != nil {
		return contract.Principal{}, storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "agent_profile_set", owner, principal.ID, "owner_profile"); err != nil {
		return contract.Principal{}, storeError(err)
	}
	return principal, storeError(tx.Commit())
}

func (r *Registry) Template(ctx context.Context, query contract.TemplateRequest) (contract.AgentTemplate, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return contract.AgentTemplate{}, storeError(err)
	}
	defer tx.Rollback()
	var result contract.AgentTemplate
	var profileRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT h.id,h.org_id,a.id,a.owner_id,a.org_id,?,p.restrictions,coalesce(o.shared_space,'')"+actingProfileFrom, query.Space, query.Subject, query.Actor, query.Space, query.Space, query.Space).Scan(
		&result.Subject.ID, &result.Subject.OrganizationID, &result.Actor.ID, &result.Actor.OwnerID, &result.Actor.OrganizationID, &result.Space, &profileRaw, &result.SharedSpace)
	if errors.Is(err, sql.ErrNoRows) {
		return result, contract.ErrForbidden
	}
	if err != nil {
		return result, storeError(err)
	}
	result.Subject.Kind = contract.Human
	result.Actor.Kind = contract.Agent
	result.OrganizationID = result.Subject.OrganizationID
	result.SpaceKind = "private"
	if result.SharedSpace != "" && result.SharedSpace == query.Space {
		result.SpaceKind = "organization"
	}
	constraints, err := inheritedLayers(ctx, tx, "platform", orgScope(result.Subject.OrganizationID), "user:"+result.Subject.ID)
	if err != nil {
		return result, err
	}
	var targetOrg string
	err = tx.QueryRowContext(ctx, "SELECT org_id FROM shared_spaces WHERE id=?", query.Space).Scan(&targetOrg)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, storeError(err)
	}
	if err == nil {
		result.OrganizationID = targetOrg
		result.SpaceKind = "shared"
	}
	if targetOrg != "" && targetOrg != result.Subject.OrganizationID {
		ceiling, err := inheritedLayers(ctx, tx, orgScope(targetOrg))
		if err != nil {
			return result, err
		}
		constraints = compose(constraints, ceiling)
	}
	var profile contract.Restrictions
	if json.Unmarshal(profileRaw, &profile) != nil {
		return result, contract.ErrUnavailable
	}
	profile, err = normalizeRestrictions(profile)
	if err != nil {
		return result, contract.ErrUnavailable
	}
	if profile.Mode == "" {
		profile.Mode = "ask-critical"
	}
	result.Restrictions = compose(constraints, profile)
	return result, storeError(tx.Commit())
}
