package toolhub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// contractCoverable reports whether a credential input is collected through
// the broker form and delivered by a contract delivery. Header, brokered and
// oauth-minted inputs are satisfied by other planes — the remote client's
// header promotion on the reviewed origin, the bounded-cli cell cred proxy,
// the OAuth flow — so they never need a contract.
func contractCoverable(input api.CredentialInput) bool {
	switch input.Delivery {
	case "", "env", "file", "json":
		return true
	default:
		return false
	}
}

// contractFieldID derives a broker field id from a declared credential name.
func contractFieldID(name string) string {
	id := strings.ToLower(name)
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	id = strings.Trim(b.String(), "_")
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = "f_" + id
	}
	if len(id) > 40 {
		sum := sha256.Sum256([]byte(name))
		id = id[:31] + "_" + hex.EncodeToString(sum[:4])
	}
	return id
}

// synthesizeContract builds a collect-and-deliver contract from the reviewed
// definition's declared credential inputs. The id is content-addressed, so
// re-review and retries stay idempotent and identical credential shapes share
// one contract.
func synthesizeContract(definition api.ToolDefinition) (contract.Contract, error) {
	fields := make([]contract.Field, 0, len(definition.Credentials))
	deliveries := make([]contract.Delivery, 0, len(definition.Credentials))
	used := map[string]bool{}
	for _, input := range definition.Credentials {
		if input.PerRequest || !contractCoverable(input) {
			continue
		}
		fid := contractFieldID(input.Name)
		if used[fid] {
			sum := sha256.Sum256([]byte(input.Name))
			fid = fid[:min(len(fid), 31)] + "_" + hex.EncodeToString(sum[:4])
		}
		used[fid] = true
		// A contract field cannot express either/or: grouped alternatives are
		// optional in the contract; the definition-level group check decides
		// completeness.
		required := input.Required && !api.GroupedCredentialInput(definition, input.Name)
		kind := "secret"
		switch input.Delivery {
		case "file":
			kind = "blob"
		case "json":
			kind = "json"
		}
		fields = append(fields, contract.Field{ID: fid, Label: input.Name, Kind: kind, Required: required, MaxBytes: 16384})
		if input.Delivery == "file" {
			// The value mounts read-only at Target; the env var carries the
			// in-container path, matching the local materializer's contract.
			deliveries = append(deliveries, contract.Delivery{Type: "file", Field: fid, Target: input.Target, EnvName: input.Name})
			continue
		}
		deliveries = append(deliveries, contract.Delivery{Type: "env", Field: fid, Target: input.Name})
	}
	canonical, _ := json.Marshal(struct {
		F []contract.Field    `json:"f"`
		D []contract.Delivery `json:"d"`
	}{fields, deliveries})
	// A definition pinning a contract missing from the broker catalog registers
	// the synthesized contract under that pinned id; otherwise the id is
	// content-addressed so retries and identical credential shapes dedupe.
	id := definition.CredentialContractID
	revision := definition.CredentialContractRevision
	if id == "" {
		digest := sha256.Sum256(canonical)
		id = "dyn-" + hex.EncodeToString(digest[:])[:24]
	}
	if revision < 1 {
		revision = 1
	}
	title := "credentials for " + definition.DefinitionID
	if len(title) > 120 {
		title = title[:120]
	}
	return contract.Contract{ID: id, Revision: revision, Title: title, Fields: fields, Deliveries: deliveries, AllowShared: definition.Workload.Class == api.Shared && definition.Workload.Sharable}, nil
}

// brokerExtras lists declared optional inputs the bound contract does not
// deliver. They ride on the credential request as request-scoped extras, so
// the same protected form collects them and entered values materialize as env
// without ever rewriting the reviewed contract. Extras must never shadow the
// bound contract — a field id or delivery target collision makes the broker
// reject the whole request — so inputs whose target the contract already
// delivers are dropped and inputs whose derived field id collides are
// renamed deterministically.
func brokerExtras(definition api.ToolDefinition) *contract.Extra {
	fieldIDs, targets := boundContractShape(definition)
	extra := &contract.Extra{}
	used := map[string]bool{}
	for _, input := range definition.Credentials {
		if input.Required || input.PerRequest || !contractCoverable(input) || definition.CredentialContractEnv[input.Name] != "" || targets[input.Name] {
			continue
		}
		fid := contractFieldID(input.Name)
		if fieldIDs[fid] {
			sum := sha256.Sum256([]byte(input.Name))
			fid = fid[:min(len(fid), 31)] + "_" + hex.EncodeToString(sum[:4])
		}
		if used[fid] || fieldIDs[fid] || fid == "csrf" {
			continue
		}
		used[fid] = true
		kind := "string"
		if api.SecretName(input.Name) {
			kind = "secret"
		}
		extra.Fields = append(extra.Fields, contract.Field{ID: fid, Label: input.Name, Kind: kind, MaxBytes: 16384})
		extra.Deliveries = append(extra.Deliveries, contract.Delivery{Type: "env", Field: fid, Target: input.Name})
	}
	if len(extra.Fields) == 0 {
		return nil
	}
	return extra
}

// boundContractShape returns the field ids and delivery targets of the
// contract the definition is bound to, when that shape is locally known
// (embedded reviewed contract). Catalog contracts bound by selection mark
// every deliverable input through CredentialContractEnv, so an empty shape
// is safe for them.
func boundContractShape(definition api.ToolDefinition) (map[string]bool, map[string]bool) {
	fieldIDs, targets := map[string]bool{}, map[string]bool{}
	if definition.CredentialContractID == "" {
		return fieldIDs, targets
	}
	reviewed, ok, err := api.PreparedContractFor(definition.CredentialContractID, definition.CredentialContractRevision)
	if err != nil || !ok {
		return fieldIDs, targets
	}
	for _, f := range reviewed.Fields {
		fieldIDs[f.ID] = true
	}
	for _, d := range reviewed.Deliveries {
		target := d.Target
		if d.Type != "env" {
			target = d.EnvName
		}
		if target != "" {
			targets[target] = true
		}
	}
	return fieldIDs, targets
}
