package identityhub

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

const signingSchema = `
CREATE TABLE IF NOT EXISTS signing_keys (
 id TEXT PRIMARY KEY, purpose TEXT NOT NULL CHECK(purpose IN ('run','link','service','policy')),
 public BLOB NOT NULL CHECK(length(public)=32), seed BLOB,
 verify_until INTEGER NOT NULL DEFAULT 0,
 CHECK((verify_until=0 AND seed IS NOT NULL AND length(seed)=32) OR (verify_until>0 AND seed IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS active_signing_key ON signing_keys(purpose) WHERE verify_until=0;
CREATE TABLE IF NOT EXISTS policy_bundles (digest TEXT PRIMARY KEY, canonical BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS active_passport_bundles (
 space TEXT NOT NULL, environment TEXT NOT NULL, digest TEXT NOT NULL REFERENCES policy_bundles(digest),
 PRIMARY KEY(space,environment)
);
`

var signingPurposes = [...]contract.SigningPurpose{contract.SignRun, contract.SignLink, contract.SignService, contract.SignPolicy}

// EnsureSigningKeys initializes the control issuer's four independent keys.
func (r *Registry) EnsureSigningKeys(ctx context.Context) error {
	return r.updateSigningKeys(ctx, 0)
}

// RotateSigningKeys keeps the former public keys for one maximum run budget.
func (r *Registry) RotateSigningKeys(ctx context.Context, maximumRunBudget time.Duration) error {
	if maximumRunBudget < time.Second {
		return contract.ErrInvalid
	}
	return r.updateSigningKeys(ctx, maximumRunBudget)
}

func (r *Registry) IssueAutomationService(ctx context.Context, request contract.ServiceRequest) (string, error) {
	return r.issueService(ctx, "system:communication-hub", contract.AutomationServiceAudience, request)
}

func (r *Registry) IssueMemoryContextService(ctx context.Context, request contract.ServiceRequest) (string, error) {
	return r.issueService(ctx, "system:hub-control", contract.MemoryContextServiceAudience, request)
}

func (r *Registry) IssueMemoryCaptureService(ctx context.Context, request contract.ServiceRequest) (string, error) {
	return r.issueService(ctx, "system:communication-hub", contract.MemoryCaptureServiceAudience, request)
}

func (r *Registry) IssuePersonalModelService(ctx context.Context, request contract.ServiceRequest) (string, error) {
	if request.PolicyRevision != "" {
		return "", contract.ErrInvalid
	}
	return r.issueModelService(ctx, contract.PersonalModelPrincipal, contract.PersonalModelAudience, request)
}

func (r *Registry) IssueDreamingModelService(ctx context.Context, request contract.ServiceRequest) (string, error) {
	if !request.Valid() || !contract.ValidPolicyDigest(request.PolicyRevision) {
		return "", contract.ErrInvalid
	}
	principal, err := r.Principal(ctx, request.Space)
	if err != nil || principal.Kind != contract.Human {
		return "", contract.ErrForbidden
	}
	return r.issueModelService(ctx, contract.DreamingModelPrincipal, contract.DreamingModelAudience, request)
}

func (r *Registry) issueModelService(ctx context.Context, service, audience string, request contract.ServiceRequest) (string, error) {
	if !request.Valid() {
		return "", contract.ErrInvalid
	}
	digest, err := r.ActiveServicePassports(ctx, request.Space, request.Environment)
	if err != nil {
		return "", err
	}
	if request.PolicyRevision != "" && request.PolicyRevision != digest {
		return "", contract.ErrForbidden
	}
	policy, err := r.verifiedPolicy(ctx, digest, request.Space, request.Environment)
	if err != nil || !policy.AllowsModelService(audience) {
		return "", contract.ErrForbidden
	}
	request.PolicyRevision = digest
	return r.issueService(ctx, service, audience, request)
}

func (r *Registry) issueService(ctx context.Context, service, audience string, request contract.ServiceRequest) (string, error) {
	if !request.Valid() {
		return "", contract.ErrInvalid
	}
	return r.sign(ctx, contract.SignService, contract.ServiceClaims{Issuer: contract.RunTokenIssuer,
		Service: service, Audience: audience, ServiceRequest: request,
		Expires: r.now().Add(30 * time.Second).Unix()})
}

func (r *Registry) updateSigningKeys(ctx context.Context, overlap time.Duration) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	now := r.now()
	if overlap != 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE signing_keys SET verify_until=?,seed=NULL WHERE verify_until=0", now.Add(overlap).Unix()); err != nil {
			return storeError(err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM signing_keys WHERE verify_until>0 AND verify_until<=?", now.Unix()); err != nil {
		return storeError(err)
	}
	for _, purpose := range signingPurposes {
		if err := ensureSigningKey(ctx, tx, purpose); err != nil {
			return err
		}
	}
	if overlap != 0 {
		if err := audit(ctx, tx, now, "signing_keys_rotated", "operator:supervisor", "identity-signing", "run_budget_overlap"); err != nil {
			return storeError(err)
		}
	}
	return storeError(tx.Commit())
}

func ensureSigningKey(ctx context.Context, tx *sql.Tx, purpose contract.SigningPurpose) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM signing_keys WHERE purpose=? AND verify_until=0)", purpose).Scan(&exists); err != nil {
		return storeError(err)
	}
	if exists {
		return nil
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return contract.ErrUnavailable
	}
	defer clear(private)
	seed := private.Seed()
	defer clear(seed)
	_, err = tx.ExecContext(ctx, "INSERT INTO signing_keys(id,purpose,public,seed) VALUES(?,?,?,?)", contract.SigningKeyID(public), purpose, []byte(public), seed)
	return storeError(err)
}

func (r *Registry) SigningKeys(ctx context.Context) ([]contract.PublicSigningKey, error) {
	return signingKeys(ctx, r.db, r.now())
}

func signingKeys(ctx context.Context, database interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, now time.Time) ([]contract.PublicSigningKey, error) {
	rows, err := database.QueryContext(ctx, "SELECT id,purpose,public,verify_until FROM signing_keys WHERE verify_until=0 OR verify_until>? ORDER BY purpose,verify_until,id", now.Unix())
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	var keys []contract.PublicSigningKey
	for rows.Next() {
		var key contract.PublicSigningKey
		if err := rows.Scan(&key.ID, &key.Purpose, &key.Public, &key.VerifyUntil); err != nil {
			return nil, storeError(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError(err)
	}
	if len(keys) == 0 {
		return nil, contract.ErrUnavailable
	}
	return keys, nil
}

// IssueRun is called by control after it binds the durable admission to the
// actual runtime run ID. Consumers still consult live authority at dispatch.
func (r *Registry) IssueRun(ctx context.Context, claims contract.RunClaims, deadline time.Time, maximumRunBudget time.Duration) (string, error) {
	now := r.now()
	claims.Issuer = contract.RunTokenIssuer
	claims.Expires = min(deadline.Unix(), now.Add(maximumRunBudget).Unix())
	if !claims.Valid() || claims.Expires <= now.Unix() {
		return "", contract.ErrInvalid
	}
	policy, err := r.verifiedPolicy(ctx, claims.PolicyRevision, claims.Space, claims.Environment)
	if err != nil {
		return "", err
	}
	if policy.Identity == nil {
		return "", contract.ErrForbidden
	}
	active, err := r.ActivePassports(ctx, claims.Space, claims.Environment)
	if err != nil {
		return "", err
	}
	template, err := r.Template(ctx, contract.TemplateRequest{Subject: claims.Subject, Actor: claims.Actor, Space: claims.Space})
	if err != nil {
		return "", err
	}
	org := template.OrganizationID
	if org == "" {
		org = "personal"
	}
	if active != policy.PassportDigest || claims.Organization != org || claims.Mode != template.Restrictions.Mode || !reflect.DeepEqual(template, *policy.Identity) {
		return "", contract.ErrForbidden
	}
	return r.sign(ctx, contract.SignRun, claims)
}

// StagePassports stores one publisher-compiled bundle before activation.
func (r *Registry) StagePassports(ctx context.Context, bundle contract.PolicyBundle) (contract.SignedPolicy, error) {
	if bundle.Identity != nil {
		return contract.SignedPolicy{}, contract.ErrInvalid
	}
	return r.storePolicy(ctx, bundle)
}

func (r *Registry) ActivePassports(ctx context.Context, space, environment string) (string, error) {
	var digest string
	err := r.db.QueryRowContext(ctx, "SELECT digest FROM active_passport_bundles WHERE space=? AND environment=?", space, environment).Scan(&digest)
	return digest, storeError(err)
}

func (r *Registry) ActiveServicePassports(ctx context.Context, space, environment string) (string, error) {
	var digest string
	err := r.db.QueryRowContext(ctx, `SELECT b.digest FROM active_passport_bundles b
 WHERE b.space=? AND b.environment=? AND (
 EXISTS(SELECT 1 FROM spaces s JOIN principals h ON h.id=s.human WHERE s.id=b.space AND h.kind='human' AND h.enabled=1)
 OR EXISTS(SELECT 1 FROM shared_spaces s WHERE s.id=b.space AND s.enabled=1)
 OR EXISTS(SELECT 1 FROM organizations o WHERE o.shared_space=b.space))`, space, environment).Scan(&digest)
	return digest, storeError(err)
}

// ActivatePassports changes the authoritative pointer only if the publisher's
// base is still active. An empty next digest is permitted for first-apply rollback.
func (r *Registry) ActivatePassports(ctx context.Context, space, environment, previous, next string) error {
	if next != "" {
		bundle, err := r.verifiedPolicy(ctx, next, space, environment)
		if err != nil || bundle.Identity != nil {
			return contract.ErrForbidden
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return storeError(err)
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM active_passport_bundles WHERE space=? AND environment=?", space, environment).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return storeError(err)
	}
	if current != previous {
		return contract.ErrConflict
	}
	if next == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM active_passport_bundles WHERE space=? AND environment=?", space, environment)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO active_passport_bundles(space,environment,digest) VALUES(?,?,?) ON CONFLICT(space,environment) DO UPDATE SET digest=excluded.digest", space, environment, next)
	}
	if err != nil {
		return storeError(err)
	}
	return storeError(tx.Commit())
}

// BindPassports derives a run policy from the active signed publication and
// the registry's current intersection of identity layers for the acting agent.
func (r *Registry) BindPassports(ctx context.Context, request contract.TemplateRequest, environment string) (contract.SignedPolicy, contract.PolicyBundle, error) {
	digest, err := r.ActivePassports(ctx, request.Space, environment)
	if err != nil {
		return contract.SignedPolicy{}, contract.PolicyBundle{}, err
	}
	bundle, err := r.verifiedPolicy(ctx, digest, request.Space, environment)
	if err != nil || bundle.Identity != nil {
		return contract.SignedPolicy{}, contract.PolicyBundle{}, contract.ErrForbidden
	}
	template, err := r.Template(ctx, request)
	if err != nil {
		return contract.SignedPolicy{}, contract.PolicyBundle{}, err
	}
	bundle.PassportDigest, bundle.Identity = digest, &template
	signed, err := r.storePolicy(ctx, bundle)
	return signed, bundle, err
}

func (r *Registry) verifiedPolicy(ctx context.Context, digest, space, environment string) (contract.PolicyBundle, error) {
	policy, err := r.Policy(ctx, digest)
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	keys, err := r.SigningKeys(ctx)
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	return contract.VerifyPolicyBundle(policy.JWS, digest, space, environment, keys, r.now())
}

func (r *Registry) storePolicy(ctx context.Context, bundle contract.PolicyBundle) (contract.SignedPolicy, error) {
	body, digest, err := bundle.Canonical()
	if err != nil {
		return contract.SignedPolicy{}, err
	}
	signed, err := r.sign(ctx, contract.SignPolicy, json.RawMessage(body))
	if err != nil {
		return contract.SignedPolicy{}, err
	}
	if _, err := r.db.ExecContext(ctx, "INSERT INTO policy_bundles(digest,canonical) VALUES(?,?) ON CONFLICT(digest) DO NOTHING", digest, body); err != nil {
		return contract.SignedPolicy{}, storeError(err)
	}
	return contract.SignedPolicy{Digest: digest, JWS: signed}, nil
}

func (r *Registry) Policy(ctx context.Context, digest string) (contract.SignedPolicy, error) {
	if !contract.ValidPolicyDigest(digest) {
		return contract.SignedPolicy{}, contract.ErrInvalid
	}
	var body []byte
	err := r.db.QueryRowContext(ctx, "SELECT canonical FROM policy_bundles WHERE digest=?", digest).Scan(&body)
	if err != nil {
		return contract.SignedPolicy{}, storeError(err)
	}
	if contract.PolicyDigest(body) != digest {
		return contract.SignedPolicy{}, contract.ErrUnavailable
	}
	// The content is immutable across key rotations. Project it with the
	// current policy key so historical digests remain verifiable.
	signed, err := r.sign(ctx, contract.SignPolicy, json.RawMessage(body))
	if err != nil {
		return contract.SignedPolicy{}, err
	}
	return contract.SignedPolicy{Digest: digest, JWS: signed}, nil
}

func (r *Registry) sign(ctx context.Context, purpose contract.SigningPurpose, claims any) (string, error) {
	return sign(ctx, r.db, purpose, claims)
}

func sign(ctx context.Context, database interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, purpose contract.SigningPurpose, claims any) (string, error) {
	var id string
	var seed []byte
	err := database.QueryRowContext(ctx, "SELECT id,seed FROM signing_keys WHERE purpose=? AND verify_until=0", purpose).Scan(&id, &seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", contract.ErrUnavailable
	}
	defer clear(seed)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if contract.SigningKeyID(key[ed25519.SeedSize:]) != id {
		return "", contract.ErrUnavailable
	}
	header, err := json.Marshal(contract.SignatureHeader{Algorithm: "EdDSA", Type: purpose.Type(), KeyID: id})
	if err != nil {
		return "", contract.ErrUnavailable
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", contract.ErrInvalid
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(header) + "." + encode(payload)
	return input + "." + encode(ed25519.Sign(key, []byte(input))), nil
}
