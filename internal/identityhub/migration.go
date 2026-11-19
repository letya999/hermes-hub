package identityhub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

type legacyUser struct {
	ID       string                     `json:"id"`
	Enabled  bool                       `json:"enabled"`
	Channels []contract.ChannelIdentity `json:"channels"`
	Subject  string                     `json:"subject,omitempty"`
}

type LegacySharedSpace struct {
	ID      string
	Members []int64
}
type LegacyConfiguration struct {
	Name          string
	Organization  string
	Issuer        string
	Users         []commodel.User
	SharedSpaces  []LegacySharedSpace
	VerifiedLinks []contract.OIDCLink
}

func canonicalChannel(c contract.ChannelIdentity) bool {
	if len(c.ID) > 256 {
		return false
	}
	switch c.Channel {
	case contract.Telegram:
		id, err := strconv.ParseInt(c.ID, 10, 64)
		return err == nil && id != 0 && strconv.FormatInt(id, 10) == c.ID
	case contract.Slack:
		team, user, ok := strings.Cut(c.ID, "/")
		return ok && team != "" && user != "" && !strings.ContainsAny(c.ID, "\r\n\t ") && !strings.Contains(user, "/") && c.ID == (commodel.SlackLink{TeamID: team, UserID: user}).Key()
	}
	return false
}

// ImportLegacy projects allowlisted identity fields only. Paths, environment,
// credentials, features and runtime grants never enter the import or its digest.
func (r *Registry) ImportLegacy(ctx context.Context, name, org string, users []commodel.User) ([]Mapping, error) {
	return r.ImportLegacyConfiguration(ctx, LegacyConfiguration{Name: name, Organization: org, Users: users})
}

func (r *Registry) ImportLegacyConfiguration(ctx context.Context, input LegacyConfiguration) ([]Mapping, error) {
	name, org, users := input.Name, input.Organization, input.Users
	if !identity.ValidID(name) || !identity.ValidID(org) || (len(users) == 0 && len(input.VerifiedLinks) == 0) {
		return nil, contract.ErrInvalid
	}
	organization := org
	if organization == "personal" {
		organization = ""
	}
	issuer := input.Issuer
	if issuer != "" && !canonicalIssuer(issuer) {
		return nil, contract.ErrInvalid
	}
	proofs := append([]contract.OIDCLink(nil), input.VerifiedLinks...)
	for _, proof := range proofs {
		if issuer == "" || proof.Issuer != issuer || !validOIDCSubject(proof.Subject) || !identity.ValidID(proof.PrincipalID) || !canonicalChannel(proof.Channel) {
			return nil, contract.ErrInvalid
		}
	}
	slices.SortFunc(proofs, func(a, b contract.OIDCLink) int {
		return strings.Compare(a.Channel.Channel+"/"+a.Channel.ID+"/"+a.PrincipalID+"/"+a.Subject, b.Channel.Channel+"/"+b.Channel.ID+"/"+b.PrincipalID+"/"+b.Subject)
	})
	proofs = slices.Compact(proofs)
	shared := append([]LegacySharedSpace(nil), input.SharedSpaces...)
	sharedIDs := map[string]bool{}
	for i := range shared {
		if !identity.ValidID(shared[i].ID) || sharedIDs[shared[i].ID] || shared[i].ID == organization {
			return nil, contract.ErrInvalid
		}
		sharedIDs[shared[i].ID] = true
		members := append([]int64(nil), shared[i].Members...)
		if len(members) == 0 {
			for _, u := range users {
				if u.Enabled {
					members = append(members, u.TelegramIDs...)
				}
			}
		}
		for _, id := range members {
			if id <= 0 {
				return nil, contract.ErrInvalid
			}
		}
		slices.Sort(members)
		shared[i].Members = slices.Compact(members)
	}
	slices.SortFunc(shared, func(a, b LegacySharedSpace) int { return strings.Compare(a.ID, b.ID) })
	projected := make([]legacyUser, 0, len(users))
	seenUsers, seenChannels := map[string]bool{}, map[contract.ChannelIdentity]string{}
	disabledChannels := map[contract.ChannelIdentity]bool{}
	for _, u := range users {
		if !identity.ValidID(u.ID) || seenUsers[u.ID] {
			return nil, contract.ErrInvalid
		}
		seenUsers[u.ID] = true
		if sharedIDs[u.ID] && (len(u.TelegramIDs) != 0 || len(u.SlackIDs) != 0 || u.OIDCSubject != "") {
			return nil, contract.ErrInvalid
		}
		if u.OIDCSubject != "" && (issuer == "" || !validOIDCSubject(u.OIDCSubject)) {
			return nil, contract.ErrInvalid
		}
		channels := make([]contract.ChannelIdentity, 0, len(u.TelegramIDs)+len(u.SlackIDs))
		for _, id := range u.TelegramIDs {
			channels = append(channels, contract.ChannelIdentity{Channel: contract.Telegram, ID: strconv.FormatInt(id, 10)})
		}
		for _, link := range u.SlackIDs {
			channels = append(channels, contract.ChannelIdentity{Channel: contract.Slack, ID: link.Key()})
		}
		slices.SortFunc(channels, func(a, b contract.ChannelIdentity) int {
			return strings.Compare(a.Channel+"/"+a.ID, b.Channel+"/"+b.ID)
		})
		channels = slices.Compact(channels)
		for _, c := range channels {
			if !u.Enabled {
				disabledChannels[c] = true
				continue
			}
			if !canonicalChannel(c) {
				return nil, contract.ErrInvalid
			}
			if owner := seenChannels[c]; owner != "" && owner != u.ID {
				return nil, contract.ErrConflict
			}
			seenChannels[c] = u.ID
		}
		projected = append(projected, legacyUser{ID: u.ID, Enabled: u.Enabled, Channels: channels, Subject: u.OIDCSubject})
	}
	for id := range sharedIDs {
		if !seenUsers[id] {
			return nil, contract.ErrInvalid
		}
	}
	slices.SortFunc(projected, func(a, b legacyUser) int { return strings.Compare(a.ID, b.ID) })
	if organization != "" && seenUsers[organization] {
		return nil, contract.ErrConflict
	}
	body, err := json.Marshal(struct {
		Organization  string
		Users         []legacyUser
		Issuer        string              `json:",omitempty"`
		SharedSpaces  []LegacySharedSpace `json:",omitempty"`
		VerifiedLinks []contract.OIDCLink `json:",omitempty"`
	}{organization, projected, issuer, shared, proofs})
	if err != nil {
		return nil, contract.ErrInvalid
	}
	digest := sha256.Sum256(body)
	fingerprint := hex.EncodeToString(digest[:])
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storeError(err)
	}
	defer tx.Rollback()
	var previous string
	var mappingsJSON []byte
	err = tx.QueryRowContext(ctx, "SELECT digest,mappings FROM imports WHERE id=?", name).Scan(&previous, &mappingsJSON)
	if err == nil {
		if previous != fingerprint {
			return nil, contract.ErrConflict
		}
		var mappings []Mapping
		if json.Unmarshal(mappingsJSON, &mappings) != nil {
			return nil, contract.ErrUnavailable
		}
		return mappings, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storeError(err)
	}
	if organization != "" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM spaces WHERE id=?)+(SELECT count(*) FROM shared_spaces WHERE id=?)", organization, organization).Scan(&count); err != nil {
			return nil, storeError(err)
		}
		if count != 0 {
			return nil, contract.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO organizations(id,shared_space) VALUES(?,?) ON CONFLICT DO NOTHING", organization, organization); err != nil {
			return nil, storeError(err)
		}
	}
	mappings := make([]Mapping, 0, len(projected))
	for _, u := range projected {
		if sharedIDs[u.ID] {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM organizations WHERE shared_space=?", u.ID).Scan(&count); err != nil {
			return nil, storeError(err)
		}
		if count != 0 {
			return nil, contract.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM shared_spaces WHERE id=?", u.ID).Scan(&count); err != nil {
			return nil, storeError(err)
		}
		if count != 0 {
			return nil, contract.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM principals WHERE id=?", u.ID).Scan(&count); err != nil {
			return nil, storeError(err)
		}
		if count != 0 {
			return nil, contract.ErrConflict
		}
		agent := "agent:" + u.ID + ":default"
		if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,org_id,enabled) VALUES(?,'human',?,?)", u.ID, organization, u.Enabled); err != nil {
			return nil, storeError(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO principals(id,kind,owner_id,org_id,enabled) VALUES(?,'agent',?,?,?)", agent, u.ID, organization, u.Enabled); err != nil {
			return nil, storeError(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO profiles(id,name,space,restrictions) VALUES(?,'default',?,'{}')", agent, u.ID); err != nil {
			return nil, storeError(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO spaces(id,human,agent) VALUES(?,?,?)", u.ID, u.ID, agent); err != nil {
			return nil, storeError(err)
		}
		if organization != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO memberships(principal_id,scope,role) VALUES(?,?,'member')", u.ID, "org:"+organization); err != nil {
				return nil, storeError(err)
			}
		}
		if u.Subject != "" {
			if err := bindSubject(ctx, tx, issuer, u.Subject, u.ID); err != nil {
				return nil, err
			}
		}
		for _, c := range u.Channels {
			if !u.Enabled {
				continue
			}
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE channel=? AND external_id=? AND state='verified'", c.Channel, c.ID).Scan(&count); err != nil {
				return nil, storeError(err)
			}
			if count != 0 {
				return nil, contract.ErrConflict
			}
			linkID, err := randomID()
			if err != nil {
				return nil, contract.ErrUnavailable
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO links(id,channel,external_id,principal_id,state,expires_at) VALUES(?,?,?,?,'pending',0)", linkID, c.Channel, c.ID, u.ID); err != nil {
				return nil, storeError(err)
			}
			if err := transitionLink(ctx, tx, linkID, "verify"); err != nil {
				return nil, err
			}
		}
		mappings = append(mappings, Mapping{Space: u.ID, Human: u.ID, Agent: agent})
	}
	for _, group := range shared {
		var enabled bool
		for _, u := range projected {
			if u.ID == group.ID {
				enabled = u.Enabled
				break
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM spaces WHERE id=?)+(SELECT count(*) FROM shared_spaces WHERE id=?)+(SELECT count(*) FROM organizations WHERE shared_space=?)+(SELECT count(*) FROM principals WHERE id=?)", group.ID, group.ID, group.ID, group.ID).Scan(&count); err != nil {
			return nil, storeError(err)
		}
		if count != 0 {
			return nil, contract.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO shared_spaces(id,org_id,enabled) VALUES(?,?,?)", group.ID, organization, enabled); err != nil {
			return nil, storeError(err)
		}
		for _, external := range group.Members {
			member, err := legacyGroupMember(ctx, tx, external, disabledChannels[contract.ChannelIdentity{Channel: contract.Telegram, ID: strconv.FormatInt(external, 10)}])
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO space_members(space,human) VALUES(?,?)", group.ID, member); err != nil {
				return nil, storeError(err)
			}
		}
		mappings = append(mappings, Mapping{Space: group.ID})
	}
	for _, proof := range proofs {
		if err := r.verifyOIDC(ctx, tx, proof, "legacy_oidc"); err != nil {
			return nil, err
		}
	}
	mappingsJSON, err = json.Marshal(mappings)
	if err != nil {
		return nil, contract.ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO imports(id,digest,mappings) VALUES(?,?,?)", name, fingerprint, mappingsJSON); err != nil {
		return nil, storeError(err)
	}
	if err := audit(ctx, tx, r.now(), "legacy_import", "operator", name, "identity_only"); err != nil {
		return nil, storeError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, storeError(err)
	}
	return mappings, nil
}
