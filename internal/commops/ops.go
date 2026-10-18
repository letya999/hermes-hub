// Package commops owns the operational plane split out of the
// communication gateway: the synthetic canary probe and the caller-scoped
// health block rendered by /status. The package is a leaf — it never
// imports internal/communication; every gateway capability it needs is
// declared in Deps and injected by the owner, so the ops plane cannot
// reach gateway internals beyond this contract.
package commops

import (
	"context"
	"time"
)

// Owner is the ops-plane view of the configured principal: identity plus
// the channel addresses owner-facing notices may use. The first entry of
// TelegramIDs is the owner's home chat for notices.
type Owner struct {
	ID          string
	TelegramIDs []int64
}

// Mapping is the ops-plane view of one durable job mapping — only the
// fields the health block aggregates.
type Mapping struct {
	UserID     string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	TerminalAt time.Time
}

// Totals is the ops-plane view of usage-journal aggregation.
type Totals struct {
	Jobs        int
	InputTokens int64
}

// Deps declares every capability the ops engine borrows from its owner.
// All values are evaluated at call time, so fixtures may fill gateway
// fields after engine construction.
type Deps struct {
	// Now is the owner's clock.
	Now func() time.Time
	// SpoolRoot is the durable directory that owns events/, canary.jsonl
	// and canary-state.json.
	SpoolRoot string
	// Owner returns the configured owner context (first enabled user).
	Owner func() (Owner, bool)
	// BindTask resolves-or-creates the task record for a synthetic chat.
	BindTask func(owner Owner, chatID int64) (any, error)
	// Enqueue admits a synthetic job on the given channel task.
	Enqueue func(ctx context.Context, owner Owner, task any, chatID int64, jobID, idempotency, text string) error
	// MappingStatus reads one durable job mapping's status.
	MappingStatus func(jobID string) (status string, found bool, err error)
	// Mappings lists durable mappings owned by the user (24h filtering is
	// the caller's job).
	Mappings func(userID string) []Mapping
	// UsageTotals aggregates the usage journal for a scope since a time.
	UsageTotals func(scope string, since time.Time) (Totals, error)
	// Restarts reports the owner's runtime restart count; false means the
	// surface is unreachable or unconfigured and the line omits restarts.
	Restarts func(owner Owner) (int, bool)
	// SurfaceMissingTools lists tools the persisted readiness surface no
	// longer hands out; nil means unconfigured or unreachable (the run
	// stays the ground truth).
	SurfaceMissingTools func(ctx context.Context, owner Owner) []string
	// MockLink claims the synthetic chat for the owner.
	MockLink func(chatID int64, ownerID string) error
	// MockDelivered reports whether the delivery record landed in the
	// mock outbox — the owner keeps filename conventions.
	MockDelivered func(deliveryID string) bool
	// NotifyOwner enqueues an owner-facing notice at the owner's home
	// address.
	NotifyOwner func(ctx context.Context, idempotency string, chatID int64, text string)
	// JobTimeout bounds one canary verification round.
	JobTimeout func() time.Duration
	// DeliveryGrace bounds the wait for the canary response delivery.
	DeliveryGrace func() time.Duration
	// WriteJSONAtomic durably replaces a JSON file under SpoolRoot — the
	// owner owns the durable-write primitive (tmp + fsync + rename).
	WriteJSONAtomic func(path string, value any) error
}

// Engine runs the ops plane against injected dependencies.
type Engine struct {
	Deps Deps
}

// New builds the engine; callers may also fill Engine directly.
func New(deps Deps) *Engine { return &Engine{Deps: deps} }
