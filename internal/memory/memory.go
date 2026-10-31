// Package memory is the hub-owned provider seam (ADR-0086 D15): every memory
// store — the built-in vault and the external adapters of M20.11 — sits behind
// the same Provider contract so the engine never depends on a backend. The hub
// owns scope minting, provenance and kinds; a provider stores what it is
// handed and can never stamp authority fields itself.
package memory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Kind classifies the run that produced an entry. Only interactive kinds may
// ever promote into curated layers; background kinds are capture-only.
type Kind string

const (
	KindInteractive Kind = "interactive"
	KindRoutine     Kind = "routine"
	KindAssignment  Kind = "assignment"
	KindProbe       Kind = "probe"
	KindGoal        Kind = "goal"
	KindCompaction  Kind = "compaction"
	KindSystem      Kind = "system"
)

// Provenance is who authored an entry. It is hub-stamped — providers and
// upstream content can never mint it.
type Provenance string

const (
	ProvenanceOwner     Provenance = "owner"
	ProvenanceAgent     Provenance = "agent"
	ProvenanceUntrusted Provenance = "untrusted"
	ProvenanceSystem    Provenance = "system"
)

// Entry is one captured memory record.
type Entry struct {
	ID         string     `json:"id"`
	Scope      Scope      `json:"scope"`
	Kind       Kind       `json:"kind"`
	Provenance Provenance `json:"provenance"`
	Audience   Audience   `json:"audience"`
	SourceRef  string     `json:"source_ref,omitempty"`
	Body       string     `json:"body"`
	ObservedAt time.Time  `json:"observed_at"`
}

// PromotionEligible is the combined hard gate of SPEC-0085 R1/R5: only
// interactive turns with owner/agent provenance may promote; untrusted,
// system and background kinds can never promote.
func (e Entry) PromotionEligible() bool {
	return e.Kind == KindInteractive && (e.Provenance == ProvenanceOwner || e.Provenance == ProvenanceAgent)
}

// Audience is the prompt audience an entry may be recalled into.
type Audience string

const (
	AudiencePrivate Audience = "private"
	AudienceGroup   Audience = "group"
)

// AudienceOf maps a routing scope id to a recall audience. Unknown prefixes
// resolve to the group audience — audience confinement fails closed so a
// crafted scope can never mint private context inside a shared run.
func AudienceOf(scopeID string) Audience {
	if strings.HasPrefix(scopeID, "user:") {
		return AudiencePrivate
	}
	return AudienceGroup
}

// Scope is an opaque per-(environment × principal × audience) key minted by
// the hub. Raw identifiers are never inside a provider's reach.
type Scope struct {
	Key string `json:"key"`
}

// MintScope derives the opaque provider scope key from hub-held material over
// the environment × principal × audience triple. Deterministic for a given
// (material, env, principal, audience); raw identifiers are never recoverable
// from the key alone.
func MintScope(material []byte, environment, principal, audience string) Scope {
	mac := hmac.New(sha256.New, material)
	for _, field := range []string{environment, principal, audience} {
		mac.Write([]byte(field))
		mac.Write([]byte{0})
	}
	return Scope{Key: hex.EncodeToString(mac.Sum(nil))}
}

// Caps advertises which operations a provider serves. Capability checks are
// the honest-degradation contract: an absent capability means the provider
// returns ErrUnsupported rather than a misleading empty result.
type Caps struct {
	Capture bool `json:"capture"`
	Recall  bool `json:"recall"`
	Search  bool `json:"search"`
	Write   bool `json:"write"`
	Export  bool `json:"export"`
	// Vector is reserved for the embedding search the milestone explicitly
	// rejects for the vault backend; adapters may report it.
	Vector bool `json:"vector"`
}

// Query is a scope-confined search request.
type Query struct {
	Text     string   `json:"text"`
	Audience Audience `json:"audience"`
	Limit    int      `json:"limit"`
}

// Hit is one search result: a stable evidence reference plus its snippet.
type Hit struct {
	Ref     string  `json:"ref"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// Block is a pre-turn recall result.
type Block struct {
	Text     string   `json:"text"`
	Sources  []string `json:"sources,omitempty"`
	Degraded bool     `json:"degraded,omitempty"`
}

// Actor names the authored-commit identity a write is attributed to.
type Actor string

const (
	ActorOwner  Actor = "owner"
	ActorAgent  Actor = "agent"
	ActorRollup Actor = "rollup"
	ActorUI     Actor = "ui"
	ActorSystem Actor = "system"
)

// WriteOp is one governed write: hash-checked, zone-guarded and attributed.
// WantSHA must equal the SHA-256 of the file's current content ("" when the
// file does not exist); a mismatch rejects the write and preserves the
// owner-winning on-disk version.
type WriteOp struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	WantSHA string `json:"want_sha"`
	Actor   Actor  `json:"actor"`
	Message string `json:"message,omitempty"`
}

// WriteResult reports one accepted write.
type WriteResult struct {
	SHA256 string `json:"sha256"`
}

// ErrUnsupported is the honest-empty answer for a capability a provider does
// not serve (Caps reports it absent at the same time).
var ErrUnsupported = errors.New("memory: operation unsupported by provider")

// ErrStaleHash rejects a write whose expected content hash no longer matches
// the file on disk — the concurrent (owner) version always wins.
var ErrStaleHash = errors.New("memory: stale content hash; on-disk version wins")

// Provider is the single seam every memory backend implements. The engine
// sees only this contract; provenance, audience and scope arrive minted.
type Provider interface {
	Caps(ctx context.Context) Caps
	Capture(ctx context.Context, entry Entry) (string, error)
	Recall(ctx context.Context, scope Scope) (Block, error)
	Search(ctx context.Context, scope Scope, query Query) ([]Hit, error)
	Write(ctx context.Context, op WriteOp) (WriteResult, error)
	Export(ctx context.Context, scope Scope) ([]Entry, error)
	Health(ctx context.Context) error
}

// KindForTrigger maps a job trigger to the entry kind of SPEC-0085 R2.4:
// interactive turns may promote; routine, assignment, probe, goal, compaction
// and system work is captured for audit and can never promote. Unknown
// triggers fail closed to KindSystem.
func KindForTrigger(trigger string, assignment, probe bool) Kind {
	switch {
	case probe:
		return KindProbe
	case assignment:
		return KindAssignment
	}
	switch trigger {
	case "message":
		return KindInteractive
	case "cron":
		return KindRoutine
	case "goal":
		return KindGoal
	case "compact":
		return KindCompaction
	default:
		return KindSystem
	}
}

// ProvenanceForTurn stamps turn provenance at capture (SPEC-0085 R2.3):
// the untrusted mark comes only from transport metadata, owner-authored
// instructions (messages, schedules, goals, assignments) are owner, and
// everything else the hub fires is system. Body text can never reclassify.
func ProvenanceForTurn(untrusted bool, trigger string) Provenance {
	if untrusted {
		return ProvenanceUntrusted
	}
	switch trigger {
	case "message", "cron", "goal":
		return ProvenanceOwner
	default:
		return ProvenanceSystem
	}
}

// Recalled content markers (SPEC-0085 D13/R4.4): the pre-turn recall lane
// wraps injected hits in these markers so capture can never re-extract them.
const (
	RecalledOpen  = "<!-- hermes:recalled -->"
	RecalledClose = "<!-- /hermes:recalled -->"
)

// StripRecalled removes every span from a RecalledOpen marker through its
// RecalledClose — recalled text can never re-enter a leaf as new content.
// An unclosed marker strips to end of body (fail closed: nothing after an
// open recall span is trustworthy capture material).
func StripRecalled(body string) string {
	var b strings.Builder
	rest := body
	for {
		i := strings.Index(rest, RecalledOpen)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(RecalledOpen):]
		j := strings.Index(rest, RecalledClose)
		if j < 0 {
			break
		}
		rest = rest[j+len(RecalledClose):]
	}
	return b.String()
}
