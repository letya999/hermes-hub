// Package routines owns the routines domain: schedule semantics (validation,
// calendar math, extract pipelines, durable ids) plus the engine that ticks
// schedules, dispatches occurrences, probes watchers and settles digests.
// The package is a leaf — it never imports internal/communication; every
// capability it borrows from the owner spool or gateway is declared in Deps
// and injected at wiring time.
package routines

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
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

// StoragePort is the owner's durable-record surface for the routines tree.
// Every operation must be safe for concurrent callers on its own: the
// engine invokes it inside its critical section and from lock-free read
// paths alike. Nil fields fall back to the os primitive so the owner can
// keep records in an embedded database instead of the filesystem
// (ADR-0091); WriteJSONAtomic has no fallback because atomic replace is
// the engine's only write path.
type StoragePort struct {
	// WriteJSONAtomic durably replaces a JSON file — the owner owns the
	// tmp+fsync+rename primitive.
	WriteJSONAtomic func(path string, value any) error
	// The owner accounts every filesystem mutation, including ingress hook bytes.
	WriteBytes func(path string, body []byte) error
	MkdirAll   func(path string, lifecycle bool) error
	Rename     func(from, to string) error
	Remove     func(path string) error
	ReadFile   func(path string) ([]byte, error)
	ReadDir    func(dir string) ([]string, error)
	Lstat      func(path string) (os.FileInfo, error)
}

// DispatchPort is the owner's job-admission surface. Implementations must
// serialize their own critical section internally — the engine calls them
// while holding its internal lock, so a callback must never call back into
// the engine (the only permitted lock order is engine -> owner).
type DispatchPort struct {
	// PrepareJob binds current admission authority before an occurrence persists.
	PrepareJob func(*commodel.Job) error
	// EnqueueJob admits an occurrence job to the ordinary queue.
	EnqueueJob func(commodel.Job) (bool, error)
	// LoadMapping reads one durable job mapping.
	LoadMapping func(id string) (commodel.JobMapping, error)
	// LoadTask reads one durable task under its principal.
	LoadTask func(principal, taskID string) (commodel.Task, error)
	// ListTasks lists the principal's durable tasks.
	ListTasks func(principal string) ([]commodel.Task, error)
	// TopicTask resolves the task bound to a forum topic.
	TopicTask func(principal string, chatID, topicID int64) (string, error)
}

// DeliveryPort is the owner's outbox surface — same self-serialization and
// no-call-back contract as DispatchPort.
type DeliveryPort struct {
	// EnqueueDelivery writes an outbox record idempotently.
	EnqueueDelivery func(commodel.Delivery) error
	// WriteDeliveryBlob stages one artifact body for a delivery.
	WriteDeliveryBlob func(deliveryID, name string, data []byte) (string, error)
	// NormalizeDelivery applies the delivery-plane identity rules.
	NormalizeDelivery func(commodel.Delivery) (commodel.Delivery, error)
}

// EventsPort resumes and settles "event"-kind occurrences in the owner's
// event plane. The engine always invokes these outside its own lock, so
// implementations may run I/O and take owner-side locks freely.
type EventsPort struct {
	AcquireEvents func(context.Context, commodel.RoutineOccurrence, commodel.Schedule, func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) ([]byte, error)
	ResumeEvents  func(o commodel.RoutineOccurrence, schedule commodel.Schedule, now time.Time, authorize func(commodel.Job) error) (bool, error)
	SettleEvents  func(o commodel.RoutineOccurrence, schedule commodel.Schedule, body []byte, now time.Time, probeErr error, authorize func(commodel.Job) error) ([]commodel.Event, error)
}

// Deps declares every capability the routines engine borrows from its
// owner. Invocation contexts:
//   - Hooks, Dispatch, Delivery: inside the engine critical section — the
//     implementation serializes itself and never calls back into the engine.
//   - Storage: inside or outside the engine lock — self-synchronized.
//   - Events, Signal, ChannelFor, ProbeScript, LangOf: outside the engine
//     lock — may run I/O and take owner locks.
type Deps struct {
	// Hooks resolve the two checks draft validation cannot own: provider
	// field rules and the script compiler.
	Hooks    DraftHooks
	Storage  StoragePort
	Dispatch DispatchPort
	Delivery DeliveryPort
	Events   EventsPort

	// Signal nudges the owner's wakeups after an enqueue.
	Signal func()
	// ChannelFor resolves a provider adapter for occurrence validation and
	// blocked notices; nil means the channel id is unknown.
	ChannelFor func(id string) OccurrenceChannel
	// ProbeScript resolves the owner-installed isolated script runner
	// lazily; a nil result means the runtime is unwired.
	ProbeScript func() ProbeScriptFunc
	// LangOf resolves an occurrence owner's workspace language for composed
	// delivery text; nil falls back to the hub default.
	LangOf func(principal string) locale.Lang
}

// Engine runs the routines plane against injected dependencies. It owns
// its mutex and root: the engine owns the schedules/, occurrences/,
// watchers/ and digests/ subtrees and serializes their mutation with e.mu;
// borrowed ports reach the rest of the owner's state from inside that
// section and must serialize themselves — the only permitted lock order is
// engine -> owner. Lock-free readers (LoadSchedule and friends) may run
// under any lock because every record is written atomically.
type Engine struct {
	root string
	mu   *sync.Mutex
	deps Deps
	// proactiveFlight bounds one in-flight probe/script per schedule.
	proactiveFlight map[string]bool
	// mappingDriftReported warns once per process about a corrupt active
	// occurrence binding.
	mappingDriftReported map[string]bool
	// acquireBurst holds fetched bodies for the current probe sweep only:
	// coalescing shares one fetch inside a tick and is cleared per sweep so
	// a re-due occurrence never sees a stale body (SPEC-0081 R2.1).
	acquireBurst map[string][]byte
	// acquireInflight collapses concurrent fetches for one (principal,url)
	// inside a sweep: followers wait on done and read the burst body or the
	// leader's error — the pool issues one fetch per sweep, never two.
	acquireInflight map[string]*acquireFlight
}

// NewEngine builds the engine; the owner fills every Deps field. The engine
// allocates its own mutex — no caller lock is borrowed or shared.
func NewEngine(root string, deps Deps) *Engine {
	return &Engine{root: root, mu: &sync.Mutex{}, deps: deps}
}

func (e *Engine) atomicJSON(path string, value any) error {
	return e.deps.Storage.WriteJSONAtomic(path, value)
}

func (e *Engine) normalizeDelivery(d commodel.Delivery) (commodel.Delivery, error) {
	return e.deps.Delivery.NormalizeDelivery(d)
}

func (e *Engine) channelFor(id string) OccurrenceChannel { return e.deps.ChannelFor(id) }

func (e *Engine) langOf(principal string) locale.Lang {
	if e.deps.LangOf != nil {
		return e.deps.LangOf(principal)
	}
	return locale.Fallback()
}

func (e *Engine) probeScript() ProbeScriptFunc {
	if e.deps.ProbeScript == nil {
		return nil
	}
	return e.deps.ProbeScript()
}

func (e *Engine) mkdirOwned(path string, lifecycle bool) error {
	if e.deps.Storage.MkdirAll != nil {
		return e.deps.Storage.MkdirAll(path, lifecycle)
	}
	return os.MkdirAll(path, 0700)
}
func (e *Engine) renameOwned(from, to string) error {
	if e.deps.Storage.Rename != nil {
		return e.deps.Storage.Rename(from, to)
	}
	return os.Rename(from, to)
}
func (e *Engine) removeOwned(path string) error {
	if e.deps.Storage.Remove != nil {
		return e.deps.Storage.Remove(path)
	}
	return os.Remove(path)
}
func (e *Engine) writeBytes(path string, body []byte) error {
	if e.deps.Storage.WriteBytes != nil {
		return e.deps.Storage.WriteBytes(path, body)
	}
	return os.WriteFile(path, body, 0600)
}
func (e *Engine) readFile(path string) ([]byte, error) {
	if e.deps.Storage.ReadFile != nil {
		return e.deps.Storage.ReadFile(path)
	}
	return os.ReadFile(path)
}
func (e *Engine) readDir(dir string) ([]string, error) {
	if e.deps.Storage.ReadDir != nil {
		return e.deps.Storage.ReadDir(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
func (e *Engine) lstat(path string) (os.FileInfo, error) {
	if e.deps.Storage.Lstat != nil {
		return e.deps.Storage.Lstat(path)
	}
	return os.Lstat(path)
}
