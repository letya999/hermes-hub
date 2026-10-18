// Package routines owns the routines domain: schedule semantics (validation,
// calendar math, extract pipelines, durable ids) plus the engine that ticks
// schedules, dispatches occurrences, probes watchers and settles digests.
// The package is a leaf — it never imports internal/communication; every
// capability it borrows from the owner spool or gateway is declared in Deps
// and injected at wiring time.
package routines

import (
	"context"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

// OccurrenceChannel is the provider slice the engine needs: occurrence
// admission validation and retargeting a blocked-routine notice to a safe
// address. The owner's channel registry satisfies it structurally.
type OccurrenceChannel interface {
	ValidateOccurrence(o commodel.RoutineOccurrence, caller identity.Envelope) error
	RouteBlockedNotice(item commodel.Schedule, d *commodel.Delivery)
}

// ProbeScriptFunc runs one watcher script probe inside the isolated script
// runtime.
type ProbeScriptFunc func(ctx context.Context, o commodel.RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error)

// Deps declares every capability the routines engine borrows from its owner.
// Fields marked "locked" run with the engine lock already held — the owner's
// spool mutex IS the engine lock, so the exact pre-split serialization
// (including callers that re-enter under the same lock) is preserved.
type Deps struct {
	// Hooks resolve the two checks draft validation cannot own: provider
	// field rules and the script compiler.
	Hooks DraftHooks
	// EnqueueJob admits an occurrence job to the ordinary queue. Locked.
	EnqueueJob func(commodel.Job) (bool, error)
	// EnqueueDelivery writes an outbox record idempotently. Locked.
	EnqueueDelivery func(commodel.Delivery) error
	// LoadMapping reads one durable job mapping. Locked.
	LoadMapping func(id string) (commodel.JobMapping, error)
	// LoadTask reads one durable task under its principal. Locked.
	LoadTask func(principal, taskID string) (commodel.Task, error)
	// ListTasks lists the principal's durable tasks. Locked.
	ListTasks func(principal string) ([]commodel.Task, error)
	// TopicTask resolves the task bound to a forum topic. Locked.
	TopicTask func(principal string, chatID, topicID int64) (string, error)
	// WriteDeliveryBlob stages one artifact body for a delivery. Locked.
	WriteDeliveryBlob func(deliveryID, name string, data []byte) (string, error)
	// NormalizeDelivery applies the delivery-plane identity rules. Locked.
	NormalizeDelivery func(commodel.Delivery) (commodel.Delivery, error)
	// WriteJSONAtomic durably replaces a JSON file — the owner owns the
	// tmp+fsync+rename primitive. Locked.
	WriteJSONAtomic func(path string, value any) error
	// Signal nudges the owner's wakeups after an enqueue. Locked.
	Signal func()
	// ChannelFor resolves a provider adapter for occurrence validation and
	// blocked notices; nil means the channel id is unknown.
	ChannelFor func(id string) OccurrenceChannel
	// ProbeScript resolves the owner-installed isolated script runner
	// lazily; a nil result means the runtime is unwired.
	ProbeScript func() ProbeScriptFunc
	// SettleEvents settles an "event"-kind occurrence into the owner's event
	// plane. It runs WITHOUT the engine lock — the owner re-locks internally.
	SettleEvents func(o commodel.RoutineOccurrence, schedule commodel.Schedule, body []byte, now time.Time, probeErr error, authorize func(commodel.Job) error) []commodel.Event
}

// Engine runs the routines plane against injected dependencies. It shares
// the owner spool's mutex and root: the engine owns the schedules/,
// occurrences/, watchers/ and digests/ subtrees; borrowed deps reach the
// rest of the spool under the same serialization as before the split.
type Engine struct {
	root string
	mu   *sync.Mutex
	deps Deps
	// proactiveFlight bounds one in-flight probe/script per schedule.
	proactiveFlight map[string]bool
	// mappingDriftReported warns once per process about a corrupt active
	// occurrence binding.
	mappingDriftReported map[string]bool
}

// NewEngine builds the engine; the owner fills every Deps field.
func NewEngine(root string, mu *sync.Mutex, deps Deps) *Engine {
	return &Engine{root: root, mu: mu, deps: deps}
}

func (e *Engine) atomicJSON(path string, value any) error {
	return e.deps.WriteJSONAtomic(path, value)
}

func (e *Engine) normalizeDelivery(d commodel.Delivery) (commodel.Delivery, error) {
	return e.deps.NormalizeDelivery(d)
}

func (e *Engine) channelFor(id string) OccurrenceChannel { return e.deps.ChannelFor(id) }

func (e *Engine) probeScript() ProbeScriptFunc {
	if e.deps.ProbeScript == nil {
		return nil
	}
	return e.deps.ProbeScript()
}
