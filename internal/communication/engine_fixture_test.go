package communication

// Test-only owner surface for fixtures migrated off the deleted
// communication automation machinery. A testOwner embeds a real
// communication Spool — task bindings, job admission, mappings and the
// outbox stay communication-owned and keep their production semantics —
// and wraps a real routines.Engine for the ToolHub-owned automation plane.
// Engine deps delegate job admission to the spool's durable Enqueue and
// deliveries to its outbox, the same split the production host has.
// Field names keep the old s.rt / s.fsys / s.mu shapes so migrated
// assertions stay close to their original form.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

// testOwner embeds the real communication spool and exposes the automation
// verbs the retired spool carried, implemented on a real routines.Engine.
type testOwner struct {
	*Spool
	rt          *routines.Engine
	fsys        routines.StoragePort
	root        string
	g           *Gateway
	deps        routines.Deps
	policy      automation.ChannelPolicy
	probeScript routines.ProbeScriptFunc
	langOf      func(string) locale.Lang
	channelFor  func(string) routines.OccurrenceChannel
	// mu shadows the retired spool mutex: engine state serializes itself,
	// so migrated tests locking s.mu guard nothing but stay compilable.
	mu sync.Mutex
}

func ownerFixture(t *testing.T) *testOwner {
	t.Helper()
	return ownerFixtureDeps(t, routines.Deps{})
}

func ownerFixtureDeps(t *testing.T, extra routines.Deps) *testOwner {
	t.Helper()
	// One durable root, like the retired unified spool: engine record dirs
	// and the communication outbox/tasks/mappings trees share it, so
	// migrated s.root/... path assertions keep meaning what they meant.
	root := t.TempDir()
	spool, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"occurrences", "schedules", "watchers", "digests", "assignments", "hooks"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	g := &Gateway{spool: spool, config: Config{OrganizationID: "personal", Users: []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}}, users: map[int64]User{11: {ID: "alice", Enabled: true}}}
	channels := map[string]routines.OccurrenceChannel{
		"telegram_bot": channel.NewTelegram(g.telegramDeps()),
		"mock":         channel.NewMock(g.mockDeps()),
		"slack_app":    channel.NewSlack(g.slackDeps()),
	}
	o := &testOwner{Spool: spool, root: root, g: g}
	// The production host fences channel sends and routine admission with
	// ChannelPolicy over the operator routing file + the communication
	// task/link bindings; the fixture authors the same record so the real
	// policy (not a stub) decides what a properly owned channel accepts.
	routingPath := filepath.Join(root, "routing.json")
	if body, err := json.Marshal(map[string]any{"organization_id": g.config.OrganizationID, "users": g.config.Users}); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(routingPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	o.policy = automation.ChannelPolicy{RoutingFile: routingPath, LegacyRoot: root}
	deps := routines.Deps{
		Hooks: routines.DraftHooks{
			Channel: func(in commodel.Schedule) error {
				ch, ok := channels[in.Channel]
				if !ok {
					return fmt.Errorf("unknown schedule channel %q", in.Channel)
				}
				validator, ok := ch.(interface{ ValidateSchedule(commodel.Schedule) error })
				if !ok {
					return errors.New("channel cannot validate schedules")
				}
				return validator.ValidateSchedule(in)
			},
			Script: routines.ScriptSyntaxError,
		},
		Storage: routines.StoragePort{WriteJSONAtomic: fixtureAtomicJSON},
		Dispatch: routines.DispatchPort{
			// Durable admission rides the communication job queue exactly
			// like the production path: mapping + idempotent enqueue.
			EnqueueJob: spool.Enqueue,
			LoadMapping: func(id string) (commodel.JobMapping, error) {
				spool.mu.Lock()
				defer spool.mu.Unlock()
				return spool.loadMappingLocked(id)
			},
			LoadTask: func(principal, taskID string) (commodel.Task, error) {
				spool.mu.Lock()
				defer spool.mu.Unlock()
				return spool.loadTaskLocked(principal, taskID)
			},
			ListTasks: func(principal string) ([]commodel.Task, error) {
				spool.mu.Lock()
				defer spool.mu.Unlock()
				return spool.listTasksLocked(principal)
			},
			TopicTask: func(principal string, chatID, topicID int64) (string, error) {
				spool.mu.Lock()
				defer spool.mu.Unlock()
				return spool.topicTaskLocked(principal, chatID, topicID)
			},
		},
		Delivery: routines.DeliveryPort{
			EnqueueDelivery: func(d commodel.Delivery) error {
				// The production intake stamps OwnerPrincipalID from the
				// authenticated ToolHub principal; the fixture engine is the
				// owner, so the occurrence record supplies it — a delivery
				// with no live occurrence stays ownerless and fails closed
				// at the send-time fence exactly like a forged record.
				if d.OccurrenceID != "" && d.OwnerPrincipalID == "" {
					var occ commodel.RoutineOccurrence
					if b, err := os.ReadFile(filepath.Join(root, "occurrences", commodel.FileID(d.OccurrenceID)+".json")); err == nil && json.Unmarshal(b, &occ) == nil {
						d.OwnerPrincipalID = occ.Job.PrincipalID
					}
				}
				return spool.EnqueueDelivery(d)
			},
			WriteDeliveryBlob: spool.WriteDeliveryBlob,
			NormalizeDelivery: normalizeDelivery,
		},
		Signal: func() {},
		ChannelFor: func(id string) routines.OccurrenceChannel {
			if o.channelFor != nil {
				return o.channelFor(id)
			}
			return channels[id]
		},
		LangOf: func(principal string) locale.Lang {
			if o.langOf != nil {
				return o.langOf(principal)
			}
			return locale.LangOf(g.user(principal).WorkspaceDir)
		},
	}
	// extra overrides default ports for tests that inject failures.
	if extra.Hooks.Channel != nil || extra.Hooks.Script != nil {
		deps.Hooks = extra.Hooks
	}
	if extra.Storage.WriteJSONAtomic != nil {
		deps.Storage.WriteJSONAtomic = extra.Storage.WriteJSONAtomic
	}
	if extra.Storage.WriteBytes != nil {
		deps.Storage.WriteBytes = extra.Storage.WriteBytes
	}
	if extra.Storage.MkdirAll != nil {
		deps.Storage.MkdirAll = extra.Storage.MkdirAll
	}
	if extra.Storage.Remove != nil {
		deps.Storage.Remove = extra.Storage.Remove
	}
	if extra.Storage.ReadFile != nil {
		deps.Storage.ReadFile = extra.Storage.ReadFile
	}
	if extra.Storage.ReadDir != nil {
		deps.Storage.ReadDir = extra.Storage.ReadDir
	}
	if extra.Storage.Lstat != nil {
		deps.Storage.Lstat = extra.Storage.Lstat
	}
	if extra.Dispatch.EnqueueJob != nil {
		deps.Dispatch.EnqueueJob = extra.Dispatch.EnqueueJob
	}
	if extra.Delivery.EnqueueDelivery != nil {
		deps.Delivery.EnqueueDelivery = extra.Delivery.EnqueueDelivery
	}
	if extra.Delivery.NormalizeDelivery != nil {
		deps.Delivery.NormalizeDelivery = extra.Delivery.NormalizeDelivery
	}
	if extra.Events.AcquireEvents != nil || extra.Events.ResumeEvents != nil || extra.Events.SettleEvents != nil {
		deps.Events = extra.Events
	}
	if extra.Signal != nil {
		deps.Signal = extra.Signal
	}
	if extra.ChannelFor != nil {
		deps.ChannelFor = extra.ChannelFor
	}
	if extra.ProbeScript != nil {
		deps.ProbeScript = extra.ProbeScript
	}
	if extra.LangOf != nil {
		deps.LangOf = extra.LangOf
	}
	if extra.ProbeScript == nil {
		deps.ProbeScript = func() routines.ProbeScriptFunc { return o.probeScript }
	}
	o.fsys = deps.Storage
	o.deps = deps
	o.rt = routines.NewEngine(root, deps)
	// The production gateway re-authorizes owner-bound deliveries over the
	// ToolHub control RPC and reports delivery outcomes back; the fixture
	// serves the same two ops on the real engine plus an allow-always
	// rights decide so deliverOne exercises the full send-time chain.
	control := http.NewServeMux()
	control.HandleFunc("/v1/rights/decide", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"allow": true})
	})
	control.HandleFunc("/v1/automation/operations", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Operation string            `json:"operation"`
			Caller    identity.Envelope `json:"caller"`
			Payload   json.RawMessage   `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid automation request", http.StatusBadRequest)
			return
		}
		var result any
		switch body.Operation {
		case "occurrence.authorize":
			var args struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			}
			var occ commodel.RoutineOccurrence
			allow := json.Unmarshal(body.Payload, &args) == nil && args.ID != ""
			if allow {
				b, err := os.ReadFile(filepath.Join(root, "occurrences", commodel.FileID(args.ID)+".json"))
				allow = err == nil && json.Unmarshal(b, &occ) == nil && occ.Job.ID == args.ID &&
					o.rt.OccurrenceCurrent(occ) && (args.Kind == "routine-failure" || args.Kind == "job" || occ.State == "dispatched")
			}
			result = map[string]bool{"allow": allow}
		case "delivery.outcome":
			var args struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Text   string `json:"text"`
			}
			if json.Unmarshal(body.Payload, &args) != nil || args.ID == "" {
				http.Error(w, "invalid automation request", http.StatusBadRequest)
				return
			}
			var occ commodel.RoutineOccurrence
			b, err := os.ReadFile(filepath.Join(root, "occurrences", commodel.FileID(args.ID)+".json"))
			if err != nil || json.Unmarshal(b, &occ) != nil {
				http.Error(w, "automation operation failed", http.StatusForbidden)
				return
			}
			digest := routines.RoutineFailureDigest(args.ID, args.Text)
			if args.Status == "delivered" {
				_, err = o.rt.RecordRoutineFailure(occ.ScheduleID, digest)
			} else {
				err = o.rt.ReleaseRoutineFailurePending(occ.ScheduleID, digest)
			}
			if err != nil {
				http.Error(w, "automation operation failed", http.StatusBadRequest)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	})
	server := httptest.NewServer(control)
	t.Cleanup(server.Close)
	t.Setenv("HUB_SUPERVISOR_AUTH", "fixture-supervisor")
	g.config.ToolHubURL, g.config.AutomationAuth = server.URL, "fixture-automation"
	// Same wiring New performs: routine jobs re-authorize against the live
	// occurrence before the worker claims them.
	spool.jobFence = g.routineJobLive
	return o
}

// reopen rebuilds the engine on the same durable root — the restart the
// tests simulate; the spool (jobs/tasks/outbox) is the same durable owner.
func (o *testOwner) reopen() *testOwner {
	return &testOwner{Spool: o.Spool, rt: routines.NewEngine(o.root, o.deps), fsys: o.fsys, root: o.root, g: o.g, deps: o.deps, probeScript: o.probeScript, langOf: o.langOf, channelFor: o.channelFor}
}

// authorizeOccurrence stands in for the retired gateway authorization: the
// fixture accepts what a properly owned channel would accept.
func (o *testOwner) authorizeOccurrence(commodel.Job) error { return nil }

// tickProactive runs one full owner pass — schedule materialization, due
// dispatch and due probes — replacing the retired gateway proactive tick.
// fetch may be nil for tests that never exercise URL probes.
func (o *testOwner) tickProactive(now time.Time, fetch ...func(context.Context, string) ([]byte, error)) error {
	var f func(context.Context, string) ([]byte, error)
	if len(fetch) != 0 {
		f = fetch[0]
	}
	return o.tickProactiveTool(now, f, nil)
}

// tickProactiveTool is tickProactive with a live mcp_tool call path — the
// same seam the production host wires as Config.ToolCall. Like Host.Tick,
// every stage runs even when an earlier one reported an error: a broken
// schedule pass must never cancel the due dispatch or probe work.
func (o *testOwner) tickProactiveTool(now time.Time, fetch func(context.Context, string) ([]byte, error), toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) error {
	var firstErr error
	for _, step := range []func() error{
		func() error {
			return o.rt.TickSchedules(now, "", func(_ string, job commodel.Job) error { return o.authorizeOccurrence(job) })
		},
		func() error { return o.rt.DispatchDueOccurrences(now, o.authorizeOccurrence) },
		func() error {
			return o.rt.ProbeDueOccurrencesContext(context.Background(), now, o.authorizeOccurrence, fetch, toolCall, nil)
		},
	} {
		if err := step(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (o *testOwner) CreateSchedule(in Schedule, caller identity.Envelope, nativeCron string) (Schedule, error) {
	return o.rt.CreateSchedule(in, caller, nativeCron)
}

func (o *testOwner) createScheduleAt(in Schedule, caller identity.Envelope, nativeCron string, initialValue *string, now time.Time) (Schedule, error) {
	return o.rt.CreateScheduleAt(in, caller, nativeCron, initialValue, now)
}

func (o *testOwner) ListSchedules(caller identity.Envelope) ([]Schedule, error) {
	return o.rt.ListSchedules(caller)
}

func (o *testOwner) PauseSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	return o.rt.PauseSchedule(id, caller, expected...)
}

func (o *testOwner) DeleteSchedule(id string, caller identity.Envelope, expected ...uint64) error {
	return o.rt.DeleteSchedule(id, caller, expected...)
}

func (o *testOwner) processProactive(ctx context.Context, state string, execute func(commodel.RoutineOccurrence)) error {
	return o.rt.ProcessProactive(ctx, state, execute)
}

func (o *testOwner) channelResolver() func(id string) routines.OccurrenceChannel {
	return o.deps.ChannelFor
}

func (o *testOwner) PutOccurrence(oc RoutineOccurrence, caller identity.Envelope) error {
	return o.rt.PutOccurrence(oc, caller)
}

func (o *testOwner) DispatchDueOccurrences(now time.Time, authorize func(commodel.Job) error) error {
	return o.rt.DispatchDueOccurrences(now, authorize)
}

func (o *testOwner) TickSchedules(now time.Time, nativeCron string, authorize func(commodel.Job) error) error {
	return o.rt.TickSchedules(now, nativeCron, func(_ string, job commodel.Job) error {
		if authorize == nil {
			return nil
		}
		return authorize(job)
	})
}

func (o *testOwner) ProbeDueOccurrencesContext(ctx context.Context, now time.Time, authorize func(commodel.Job) error, fetch func(context.Context, string) ([]byte, error), toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error), serviceCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) error {
	return o.rt.ProbeDueOccurrencesContext(ctx, now, authorize, fetch, toolCall, serviceCall)
}

func (o *testOwner) ProbeDueOccurrences(now time.Time, fetch func(context.Context, string) ([]byte, error)) error {
	return o.rt.ProbeDueOccurrences(now, fetch)
}

func (o *testOwner) loadWatchStateLocked(id string) (routines.WatchState, error) {
	return o.rt.LoadWatchState(id)
}

func (o *testOwner) RunDueScripts(ctx context.Context, now time.Time, authorize func(commodel.Job) error, run func(context.Context, commodel.RoutineOccurrence) ([]string, error)) error {
	return o.rt.RunDueScripts(ctx, now, authorize, run)
}

func (o *testOwner) ApplyProbeResult(result routines.ProbeResult) error {
	return o.rt.ApplyProbeResult(result)
}

func (o *testOwner) SettleProbe(oc RoutineOccurrence, sched Schedule, value, hash string, now time.Time, probeErr error, authorize func(commodel.Job) error) error {
	return o.rt.SettleProbe(oc, sched, value, hash, now, probeErr, authorize)
}

func (o *testOwner) SettleHookIdle(oc RoutineOccurrence, now time.Time, authorize func(commodel.Job) error) {
	o.rt.SettleHookIdle(oc, now, authorize)
}

func (o *testOwner) IntakeHook(scheduleID string, body []byte, now time.Time) error {
	return o.rt.IntakeHook(scheduleID, body, now)
}

func (o *testOwner) HookPendingCount(scheduleID string) int { return o.rt.HookPendingCount(scheduleID) }

func (o *testOwner) RecordRoutineFailure(scheduleID, digest string) (bool, error) {
	return o.rt.RecordRoutineFailure(scheduleID, digest)
}

func (o *testOwner) ClearRoutineFailure(scheduleID string) error {
	return o.rt.ClearRoutineFailure(scheduleID)
}

func (o *testOwner) SeedWatchBaseline(id, value string, now time.Time) error {
	return o.rt.SeedWatchBaseline(id, value, now)
}

func (o *testOwner) MutateProbingOccurrence(oc RoutineOccurrence, mutate func(live *RoutineOccurrence) error) (bool, error) {
	return o.rt.MutateProbingOccurrence(oc, mutate)
}

func (o *testOwner) OccurrenceCurrent(oc RoutineOccurrence) bool { return o.rt.OccurrenceCurrent(oc) }

func (o *testOwner) BlockSchedule(item *Schedule, reason string, now time.Time) error {
	return o.rt.BlockSchedule(item, reason, now)
}

func (o *testOwner) EditRoutine(id string, caller identity.Envelope, patch map[string]json.RawMessage, resume bool) (Schedule, error) {
	return o.editRoutine(id, caller, patch, resume, time.Now().UTC())
}

func (o *testOwner) editRoutine(id string, caller identity.Envelope, patch map[string]json.RawMessage, resume bool, now time.Time) (Schedule, error) {
	return o.rt.EditRoutine(id, caller, patch, resume, now)
}

// GetRoutine composes the owner card the retired spool served: schedule,
// lifecycle state and the durable watch row — ownership-fenced like the
// production RPC surface.
func (o *testOwner) GetRoutine(id string, caller identity.Envelope) (RoutineCard, error) {
	item, err := o.rt.LoadSchedule(id)
	if err != nil {
		return RoutineCard{}, err
	}
	if !routines.OwnsHistoryEnvelope(item, caller) {
		return RoutineCard{}, ErrRoutineNotOwned
	}
	card := RoutineCard{Schedule: item, State: routines.ScheduleState(item)}
	card.Schedule.NextDue = routines.EffectiveNextDue(item)
	if st, err := o.rt.LoadWatchState(id); err == nil {
		card.WatchState = st
	}
	return card, nil
}

func (o *testOwner) PreviewRoutine(id string, caller identity.Envelope, count int, now time.Time) (routines.RoutinePreview, error) {
	item, err := o.rt.LoadSchedule(id)
	state := routines.ScheduleState(item)
	if os.IsNotExist(err) {
		b, readErr := os.ReadFile(o.rt.CancelPath(id))
		if readErr != nil || json.Unmarshal(b, &item) != nil {
			return routines.RoutinePreview{}, ErrRoutineNotOwned
		}
		state = "deleted"
	} else if err != nil {
		return routines.RoutinePreview{}, err
	}
	if !routines.OwnsHistoryEnvelope(item, caller) {
		return routines.RoutinePreview{}, ErrRoutineNotOwned
	}
	return routines.PreviewSchedule(item, state, count, now)
}

// RoutineHistory keeps the isolation contract for migrated tests; the
// ToolHub→receipt join itself is asserted through the gateway fixture.
func (o *testOwner) RoutineHistory(id string, caller identity.Envelope) ([]RoutineRun, error) {
	item, err := o.rt.LoadSchedule(id)
	if err != nil {
		return nil, err
	}
	if !routines.OwnsHistoryEnvelope(item, caller) {
		return nil, ErrRoutineNotOwned
	}
	return nil, nil
}

func (o *testOwner) UpdateSchedule(id string, caller identity.Envelope, expression, input string, enabled *bool) (Schedule, error) {
	return o.rt.UpdateSchedule(id, caller, expression, input, enabled)
}

func (o *testOwner) SnoozeSchedule(id string, caller identity.Envelope, until time.Time, expected ...uint64) error {
	return o.rt.SnoozeSchedule(id, caller, until, expected...)
}

func (o *testOwner) LoadSchedule(id string) (Schedule, error) { return o.rt.LoadSchedule(id) }

func (o *testOwner) loadScheduleLocked(id string) (Schedule, error) {
	return o.rt.LoadSchedule(id)
}

func (o *testOwner) schedulePath(id string) string { return o.rt.SchedulePath(id) }
func (o *testOwner) watchPath(id string) string    { return o.rt.WatchPath(id) }
func (o *testOwner) digestPath(id string) string   { return o.rt.DigestPath(id) }
func (o *testOwner) cancelPath(id string) string   { return o.rt.CancelPath(id) }

// pendingDeliveries lists the spool outbox the engine posts to — the same
// durable view the retired ClaimDelivery loop read.
func (o *testOwner) pendingDeliveries() []commodel.Delivery {
	entries, err := os.ReadDir(filepath.Join(o.Spool.root, "outbox", "pending"))
	if err != nil {
		return nil
	}
	var out []commodel.Delivery
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(o.Spool.root, "outbox", "pending", e.Name()))
		if err != nil {
			continue
		}
		var d commodel.Delivery
		if json.Unmarshal(b, &d) == nil {
			out = append(out, d)
		}
	}
	return out
}

func fixtureAtomicJSON(path string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
