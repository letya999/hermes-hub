package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// recordingChannel is a plugin test double: registering it in channelPlugins
// exercises the same seam a third-party channel implementation would use.
type recordingChannel struct {
	caps        ChannelCaps
	deliveries  []*Delivery
	artifacts   []hubruntime.ArtifactRef
	presence    []JobMapping
	verifyErr   error
	prepare     func(in *Schedule, caller identity.Envelope) (identity.Envelope, error)
	routeNotice func(d *Delivery)
	envelope    func(user User, chatID, sender int64) identity.Envelope
}

func (r *recordingChannel) ID() string { return "rec_test" }

func (r *recordingChannel) NormalizeDelivery(d *Delivery) error { return nil }

func (r *recordingChannel) Deliver(ctx context.Context, d *Delivery) error {
	r.deliveries = append(r.deliveries, d)
	return nil
}

func (r *recordingChannel) AuthorizeOccurrence(job Job, user User, configured bool) error {
	return nil
}

func (r *recordingChannel) ValidateOccurrence(o RoutineOccurrence, caller identity.Envelope) error {
	return nil
}

func (r *recordingChannel) ValidateSchedule(in Schedule) error { return nil }

func (r *recordingChannel) Caps() ChannelCaps { return r.caps }

func (r *recordingChannel) SendPresence(ctx context.Context, chatID, topicID int64) error {
	r.presence = append(r.presence, JobMapping{ChatID: chatID, TopicID: topicID})
	return nil
}

func (r *recordingChannel) DeliverArtifact(ctx context.Context, d *Delivery, ref hubruntime.ArtifactRef) error {
	r.artifacts = append(r.artifacts, ref)
	return nil
}

// recordingChannel implements the optional deliveryTargetVerifier hook: a
// channel whose targets can be re-bound between enqueue and send verifies
// the owner here (mock link tables work this way).
func (r *recordingChannel) VerifyDeliveryTarget(d *Delivery, ownerID string) error {
	return r.verifyErr
}

func (r *recordingChannel) PrepareRoutine(in *Schedule, caller identity.Envelope) (identity.Envelope, error) {
	if r.prepare != nil {
		return r.prepare(in, caller)
	}
	return caller, nil
}

func (r *recordingChannel) RouteBlockedNotice(item Schedule, d *Delivery) {
	if r.routeNotice != nil {
		r.routeNotice(d)
	}
}

func (r *recordingChannel) JobEnvelope(user User, chatID, sender int64) identity.Envelope {
	if r.envelope != nil {
		return r.envelope(user, chatID, sender)
	}
	return user.Envelope(sender)
}

func registerTestChannel(t *testing.T, ch *recordingChannel) {
	t.Helper()
	channelPlugins[ch.ID()] = func(*Gateway) Channel { return ch }
	t.Cleanup(func() { delete(channelPlugins, ch.ID()) })
}

// A durable outbox record written before the registry existed (or by a
// hand-tooled restore) can name a channel no plugin provides. It must not
// silently fall through to the Telegram sender — the correct outcome is an
// uncertain marking plus a diagnostic, never a misrouted send.
func TestUnknownDeliveryChannelNeverFallsBackToTelegram(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	d := Delivery{ID: "ghost-delivery", Channel: "carrier_pigeon", ChatID: 11, Text: "stale record"}
	if err := atomicJSON(filepath.Join(g.spool.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 0 {
		t.Fatalf("unregistered channel delivered through telegram: %v", fake.sent)
	}
	pending, _ := filepath.Glob(filepath.Join(g.spool.root, "outbox", "pending", "*.json"))
	sending, _ := filepath.Glob(filepath.Join(g.spool.root, "outbox", "sending", "*.json"))
	if len(pending)+len(sending) != 0 {
		t.Fatalf("ghost delivery left in flight: pending=%v sending=%v", pending, sending)
	}
}

// Progress presence is a channel capability, not a provider name: a channel
// advertising Typing gets SendPresence for its admitted runs, no matter what
// its ID is.
func TestProgressPresenceFollowsChannelCaps(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{caps: ChannelCaps{Typing: true}}
	registerTestChannel(t, fake)
	m := JobMapping{JobID: "job-rec-1", Channel: "rec_test", RunID: "run-1", ChatID: 11, CreatedAt: time.Now().UTC()}
	if err := atomicJSON(filepath.Join(g.spool.root, "mappings", spoolFileID(m.JobID)+".json"), m); err != nil {
		t.Fatal(err)
	}
	g.progressTick(context.Background())
	if len(fake.presence) == 0 {
		t.Fatal("typing-capable channel never got SendPresence")
	}
	if fake.presence[0].ChatID != 11 {
		t.Fatalf("presence went to the wrong chat: %+v", fake.presence)
	}
}

// The coalescing editable status message (SPEC-0053 R2.2) is a channel
// capability: a plugin that can edit in place advertises EditableStatus and
// gets the "-status" delivery id; others get per-event records.
func TestEditableStatusFollowsChannelCaps(t *testing.T) {
	fake := &recordingChannel{caps: ChannelCaps{EditableStatus: true}}
	registerTestChannel(t, fake)
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	job := Job{Envelope: envelope, ID: "rec-status-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "rec_test", Trigger: "message", IdempotencyKey: "rec-status-key", ChatID: 11, Text: "hello"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	started := streamEvent(job)
	started.EventID, started.LastEvent = "started", "run.started"
	if err := s.RecordStreamEvent(job, started); err != nil {
		t.Fatal(err)
	}
	pending, _ := filepath.Glob(filepath.Join(s.root, "outbox", "pending", "job-rec-status-job-status.json"))
	if len(pending) == 0 {
		t.Fatal("editable-status-capable channel got a per-event delivery id")
	}
}

// Artifact delivery is a channel capability: the gateway iterates artifact
// refs only when the channel advertises Artifacts, and the channel itself
// decides how a ref is sent (binary upload, note, drop).
func TestArtifactsFollowChannelCaps(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{caps: ChannelCaps{Artifacts: true}}
	registerTestChannel(t, fake)
	g.api = &fakeAPI{}
	d := Delivery{ID: "rec-artifact", Channel: "rec_test", ChatID: 11, Text: "done", Artifacts: []hubruntime.ArtifactRef{{Name: "out.txt", Blob: "b1"}}}
	if err := atomicJSON(filepath.Join(g.spool.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.deliveries) == 0 {
		t.Fatal("delivery never reached the channel")
	}
	if len(fake.artifacts) != 1 || fake.artifacts[0].Name != "out.txt" {
		t.Fatalf("artifact not routed to the channel: %+v", fake.artifacts)
	}
}

// Routine destination cards are a channel capability: the runtime only
// resolves a provider-bound destination when the channel opts in.
func TestRoutineCardDestinationFollowsChannelCaps(t *testing.T) {
	fake := &recordingChannel{caps: ChannelCaps{Destinations: true}}
	registerTestChannel(t, fake)
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	item := Schedule{ScheduleID: "rec-routine", Channel: "rec_test", ChatID: 11, Envelope: caller, Timezone: "UTC", Expression: "*/5 * * * *"}
	if err := atomicJSON(filepath.Join(s.root, "schedules", spoolFileID(item.ScheduleID)+".json"), item); err != nil {
		t.Fatal(err)
	}
	card, err := s.GetRoutine("rec-routine", caller)
	if err != nil {
		t.Fatal(err)
	}
	if card.Destination == nil {
		t.Fatal("destination-capable channel got no destination card")
	}
}

// Routine-origin pinning is a channel capability: a channel that opts in
// passes origin_job_id upstream so the agent can route routine outcomes
// back to the owner.
func TestRoutineOriginFollowsChannelCaps(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{caps: ChannelCaps{RoutineOrigin: true}}
	registerTestChannel(t, fake)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	job := Job{ID: "rec-origin", Envelope: env, OrganizationID: g.config.OrganizationID, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "rec_test", ChatID: 11, Text: "fixture", Trigger: "message", IdempotencyKey: "rec-origin"}
	if err := atomicJSON(g.spool.mappingPath(job.ID), mappingFromJob(job, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	var got hubruntime.ExecuteRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: job.ID, Text: "ok"})
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, HTTP: server.Client(), Spool: g.spool}
	if _, err := runner.RunOutcome(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Instructions, `"origin_job_id":"rec-origin"`) {
		t.Fatalf("routine-origin channel lost its pin: %q", got.Instructions)
	}
	// The owner's workspace language rides every run's instructions so the
	// agent answers in it — testConfig pins alice to ru.
	if !strings.Contains(got.Instructions, "language is Русский (ru)") {
		t.Fatalf("run instructions lost the owner language hint: %q", got.Instructions)
	}
}

// A channel whose delivery targets can be re-bound after enqueue opts into
// VerifyDeliveryTarget; a failed check marks the delivery uncertain instead
// of sending to a target the owner no longer controls.
func TestDeliveryTargetVerifierMarksUncertain(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{verifyErr: errors.New("target re-bound")}
	registerTestChannel(t, fake)
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	m := mappingFromJob(Job{ID: "job-verify", Envelope: env, UserID: "alice", Channel: "rec_test"}, time.Now().UTC())
	if err := atomicJSON(g.spool.mappingPath("job-verify"), m); err != nil {
		t.Fatal(err)
	}
	d := Delivery{ID: "verify-delivery", JobID: "job-verify", Channel: "rec_test", ChatID: 11, Text: "hi"}
	if err := atomicJSON(filepath.Join(g.spool.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.deliveries) != 0 {
		t.Fatalf("re-bound target still delivered: %+v", fake.deliveries)
	}
}

// Routine admission asks the channel to finish the spec: fill a target,
// bind a provider envelope, or refuse. The gateway validates the result
// through the same shared rules afterward.
func TestRoutineAdmissionUsesChannelPrepare(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{prepare: func(in *Schedule, caller identity.Envelope) (identity.Envelope, error) {
		in.ChatID = 42
		return caller, nil
	}}
	registerTestChannel(t, fake)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"rec-prepared","channel":"rec_test","chat_id":0,"timezone":"UTC","expression":"0 * * * *","job_kind":"reminder","input":"fixture"}`)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("routine create failed: %d %s", w.Code, w.Body.String())
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	card, err := g.spool.GetRoutine("rec-prepared", caller)
	if err != nil {
		t.Fatal(err)
	}
	if card.Schedule.ChatID != 42 {
		t.Fatalf("channel prepare did not finish the spec: %+v", card.Schedule)
	}
}

// Blocked-routine notices route through the channel: a stale destination
// binding may be exactly what caused the block, so the channel decides the
// notice's safe target (Telegram rewrites to the envelope's home).
func TestBlockedNoticeRoutesThroughChannel(t *testing.T) {
	routed := false
	fake := &recordingChannel{routeNotice: func(d *Delivery) {
		routed = true
		d.ConversationID, d.DeliveryTargetID = "rec-home", "rec-home"
		d.ChatID = 7
	}}
	registerTestChannel(t, fake)
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	caller := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	item := Schedule{ScheduleID: "rec-blocked", Channel: "rec_test", ChatID: 11, Envelope: caller, Timezone: "UTC"}
	if err := atomicJSON(filepath.Join(s.root, "schedules", spoolFileID(item.ScheduleID)+".json"), item); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err = s.blockScheduleLocked(&item, "owner gone", time.Now().UTC())
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !routed {
		t.Fatal("blocked notice never reached the channel hook")
	}
	files, _ := filepath.Glob(filepath.Join(s.root, "outbox", "pending", "routine-blocked-*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one blocked notice, got %v", files)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var d Delivery
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatal(err)
	}
	if d.DeliveryTargetID != "rec-home" || d.ChatID != 7 {
		t.Fatalf("channel did not retarget the notice: %+v", d)
	}
}

// Inbound job admission takes its envelope and its typing notice from the
// channel: the provider owns identity mapping and presence signaling.
func TestInboundJobUsesChannelEnvelopeAndPresence(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &recordingChannel{
		caps: ChannelCaps{Typing: true},
		envelope: func(user User, chatID, sender int64) identity.Envelope {
			e := user.Envelope(sender)
			e.ExternalIdentityID = "rec-ext"
			return e
		},
	}
	registerTestChannel(t, fake)
	user := g.user("alice")
	task := Task{ConversationID: "conv-rec"}
	if err := g.enqueueChannelJob(context.Background(), user, task, "rec_test", "job-rec-in", "rec-key", 11, 0, "hi", 11, ""); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(g.spool.root, "pending", spoolFileID("job-rec-in")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var job Job
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	if job.Envelope.ExternalIdentityID != "rec-ext" {
		t.Fatalf("channel envelope not used: %+v", job.Envelope)
	}
	if len(fake.presence) == 0 {
		t.Fatal("typing-capable channel got no accept-time presence")
	}
}

// A spool built standalone (channelFor(nil, ...)) binds nil-gateway closures:
// RouteBlockedNotice resolves the owner through UserByPrincipal, so a nil
// gateway must report no verified home — not dereference it.
func TestNilGatewayUserByPrincipalFailsClosed(t *testing.T) {
	var g *Gateway
	if u := g.telegramDeps().UserByPrincipal("alice"); u.ID != "" {
		t.Fatalf("nil gateway resolved a user: %+v", u)
	}
}
