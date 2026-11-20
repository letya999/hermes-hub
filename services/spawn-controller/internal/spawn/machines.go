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
// active -> superseded, rollback reactivates a superseded revision, and
// rejected is the final tombstone. The doc's only terminal state is
// rejected, so the retention sweep lands there too: expire is the sweeper's
// event for a revision whose bound passed — the draft TTL the doc names,
// an activation window on validated, the revision lifetime on active
// (refresh must supersede it first) and the rollback window on superseded.
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
		{From: "active", Event: "expire", To: "rejected"},
		{From: "superseded", Event: "activate", To: "active"},
		{From: "superseded", Event: "expire", To: "rejected"},
	},
	Deadlines: map[string]fsm.Deadline{
		"draft":      {After: 10 * time.Minute, Event: "expire"},   // draft TTL; sweeper removes an uncompiled draft
		"validated":  {After: time.Hour, Event: "expire"},          // activation window on a never-activated revision
		"active":     {After: 30 * 24 * time.Hour, Event: "expire"}, // revision lifetime; refresh supersedes before it
		"superseded": {After: 24 * time.Hour, Event: "expire"},     // rollback-window retention, then tombstone
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
