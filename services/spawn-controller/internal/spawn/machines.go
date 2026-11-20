package spawn

import (
	"time"

	"github.com/letya999/hermes-hub/shared/fsm"
)

// The transition tables below implement the v1 targets of
// docs/state-machines.md (Runtime, PassportRevision) with two documented
// interpretations:
//
//   - The doc's "any" source rows fan out to every non-final state except
//     the edge's own target; final states get no edges.
//   - shared/fsm requires a deadline on every state with exits, so the
//     doc's "-" cells are filled with the bounds noted per state below.
//     Values are the S1 defaults S4's sweeper wiring may refine.

// PassportRevisionMachine is the revision lifecycle: draft -> validated ->
// active -> superseded, rollback reactivates a superseded revision,
// rejected and pruned are the final tombstones. The doc's "-" cells get
// fsm deadlines: expire sweeps an uncompiled draft (the doc's draft TTL)
// or a validated revision never activated, while retain is a self-loop
// that only re-arms the bound — an active revision is the space's only
// live policy and a superseded one is the rollback target (v0
// RestoreCurrent), so the sweeper must never tombstone either. Pruning is
// the explicit count-based edge the revision service fires: superseded
// rows beyond the v0 retention slack move to pruned, never the sweeper.
var PassportRevisionMachine = must(fsm.New(fsm.Machine{
	Table:   "passport_revisions",
	History: "passport_revisions_history",
	Initial: "draft",
	Edges: []fsm.Edge{
		{From: "draft", Event: "validate", To: "validated"},
		{From: "draft", Event: "reject", To: "rejected"},
		{From: "draft", Event: "expire", To: "rejected"},
		{From: "validated", Event: "activate", To: "active"},
		{From: "validated", Event: "expire", To: "rejected"},
		{From: "active", Event: "supersede", To: "superseded"},
		{From: "active", Event: "retain", To: "active"},
		{From: "superseded", Event: "activate", To: "active"},
		{From: "superseded", Event: "retain", To: "superseded"},
		{From: "superseded", Event: "prune", To: "pruned"},
	},
	Deadlines: map[string]fsm.Deadline{
		"draft":      {After: 10 * time.Minute, Event: "expire"},   // draft TTL; sweeper removes an uncompiled draft
		"validated":  {After: time.Hour, Event: "expire"},          // activation window on a never-activated revision
		"active":     {After: 30 * 24 * time.Hour, Event: "retain"}, // re-arm only; next actor is a newer activation
		"superseded": {After: 30 * 24 * time.Hour, Event: "retain"}, // re-arm only; next actor is a rollback
	},
}))

// RuntimeMachine is the v1 runtime lifecycle. absent is final ("final for
// this generation"): a claim creates the next generation's row in starting,
// it is not an edge out of absent. Sweeps: starting's start budget fails
// (backend reports the probe result); warm's idle TTL drains; a busy hold
// past its bound drains — a timed-out hold's runtime is reclaimed, not
// returned dirty to the warm pool; draining's stop budget quarantines
// ("stop not confirmed" is never reported as stopped); failed, quarantined
// and missing carry removal retention before their rows go absent.
var RuntimeMachine = must(fsm.New(fsm.Machine{
	Table:    "runtimes",
	History:  "runtimes_history",
	Initial:  "starting",
	Edges: []fsm.Edge{
		{From: "starting", Event: "ready", To: "warm"},
		{From: "starting", Event: "fail", To: "failed"},
		{From: "warm", Event: "lease", To: "busy"},
		{From: "busy", Event: "unlease", To: "warm"},
		{From: "warm", Event: "drain", To: "draining"},
		{From: "busy", Event: "drain", To: "draining"},
		{From: "draining", Event: "stopped", To: "absent"},
		{From: "starting", Event: "quarantine", To: "quarantined"},
		{From: "warm", Event: "quarantine", To: "quarantined"},
		{From: "busy", Event: "quarantine", To: "quarantined"},
		{From: "draining", Event: "quarantine", To: "quarantined"},
		{From: "failed", Event: "quarantine", To: "quarantined"},
		{From: "missing", Event: "quarantine", To: "quarantined"},
		{From: "starting", Event: "missing", To: "missing"},
		{From: "warm", Event: "missing", To: "missing"},
		{From: "busy", Event: "missing", To: "missing"},
		{From: "draining", Event: "missing", To: "missing"},
		{From: "failed", Event: "missing", To: "missing"},
		{From: "quarantined", Event: "missing", To: "missing"},
		{From: "failed", Event: "removed", To: "absent"},
		{From: "quarantined", Event: "removed", To: "absent"},
		{From: "missing", Event: "removed", To: "absent"},
	},
	Deadlines: map[string]fsm.Deadline{
		"starting":    {After: 30 * time.Second, Event: "fail"},       // start budget; backend probe result
		"warm":        {After: 15 * time.Minute, Event: "drain"},      // idle TTL; sweeper
		// S4: lease renewal re-arms this bound; draining a runtime removes its lease rows in the same transaction.
		"busy":        {After: 2 * time.Hour, Event: "drain"},         // hold bound; sweeper reclaims a stuck busy
		"draining":    {After: 30 * time.Second, Event: "quarantine"}, // stop budget; unconfirmed stop quarantines
		"failed":      {After: 24 * time.Hour, Event: "removed"},      // retention; reconciler or operator confirms
		"quarantined": {After: 24 * time.Hour, Event: "removed"},      // retention; operator confirms
		"missing":     {After: 24 * time.Hour, Event: "removed"},      // retention; reconciler confirms
	},
}))

// WorkerMachine is the runtime table without warm: a worker is leased at
// readiness, and a released or stuck worker drains instead of parking.
// Same sweeps as the runtime machine.
var WorkerMachine = must(fsm.New(fsm.Machine{
	Table:   "workers",
	History: "workers_history",
	Initial: "starting",
	Edges: []fsm.Edge{
		{From: "starting", Event: "ready", To: "busy"},
		{From: "starting", Event: "fail", To: "failed"},
		{From: "busy", Event: "unlease", To: "draining"},
		{From: "busy", Event: "drain", To: "draining"},
		{From: "draining", Event: "stopped", To: "absent"},
		{From: "starting", Event: "quarantine", To: "quarantined"},
		{From: "busy", Event: "quarantine", To: "quarantined"},
		{From: "draining", Event: "quarantine", To: "quarantined"},
		{From: "failed", Event: "quarantine", To: "quarantined"},
		{From: "missing", Event: "quarantine", To: "quarantined"},
		{From: "starting", Event: "missing", To: "missing"},
		{From: "busy", Event: "missing", To: "missing"},
		{From: "draining", Event: "missing", To: "missing"},
		{From: "failed", Event: "missing", To: "missing"},
		{From: "quarantined", Event: "missing", To: "missing"},
		{From: "failed", Event: "removed", To: "absent"},
		{From: "quarantined", Event: "removed", To: "absent"},
		{From: "missing", Event: "removed", To: "absent"},
	},
	Deadlines: map[string]fsm.Deadline{
		"starting":    {After: 30 * time.Second, Event: "fail"},
		"busy":        {After: 2 * time.Hour, Event: "drain"},
		"draining":    {After: 30 * time.Second, Event: "quarantine"},
		"failed":      {After: 24 * time.Hour, Event: "removed"},
		"quarantined": {After: 24 * time.Hour, Event: "removed"},
		"missing":     {After: 24 * time.Hour, Event: "removed"},
	},
}))

func must(m *fsm.Machine, err error) *fsm.Machine {
	if err != nil {
		panic(err)
	}
	return m
}
