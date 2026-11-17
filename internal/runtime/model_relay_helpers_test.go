package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The model field contract is strict: exactly one lowercase "model" key whose
// value matches the selected model, no trailing content. Anything fuzzier
// would let a caller smuggle a different upstream model.
func TestRequestsSelectedModelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		body     string
		selected string
		want     bool
	}{
		{`{"model":"synthetic"}`, "synthetic", true},
		{`{"model":"synthetic","messages":[]}`, "synthetic", true},
		{`{"model":"other"}`, "", true}, // empty selection accepts any single model
		{`{"model":"other"}`, "synthetic", false},
		{`{"Model":"synthetic"}`, "synthetic", false},
		{`{"MODEL":"synthetic"}`, "synthetic", false},
		{`{"model":"a","model":"a"}`, "a", false},
		{`{"model":"a","model":"b"}`, "a", false},
		{`{"messages":[]}`, "", false},
		{`{"model":1}`, "", false},
		{`[1,2]`, "", false},
		{`{"model":"a"} trailing`, "a", false},
		{`{"model":"a"}{"x":1}`, "a", false},
		{`{"model":`, "a", false},
		{`{"model":"a"`, "a", false},
		{``, "a", false},
	} {
		if got := requestsSelectedModel([]byte(tc.body), tc.selected); got != tc.want {
			t.Errorf("requestsSelectedModel(%q, %q) = %v, want %v", tc.body, tc.selected, got, tc.want)
		}
	}
}

// Upstream failures classify into named classes: quota and provider blocks
// get distinct ladder outcomes; everything else stays a generic route error.
func TestClassifyUpstreamErrorBoundaries(t *testing.T) {
	if class, detail := classifyUpstreamError(429, []byte(`{"error":{"message":"slow down"}}`)); class != "quota" || detail != "slow down" {
		t.Fatalf("429: %s %q", class, detail)
	}
	for _, marker := range []string{"permission_denied", "terms of service", "violation", "disabled", "suspended", "deactivated", "blocked", "banned"} {
		body := []byte(`{"error":{"status":"` + marker + `","message":"denied by policy"}}`)
		if class, _ := classifyUpstreamError(403, body); class != "provider_block" {
			t.Fatalf("403 %s: %s", marker, class)
		}
	}
	if class, _ := classifyUpstreamError(403, []byte(`{"error":{"status":"forbidden","message":"bad key"}}`)); class != "other" {
		t.Fatalf("plain 403 must not become provider_block: %s", class)
	}
	if class, detail := classifyUpstreamError(500, []byte(`{"error":{"message":"boom"}}`)); class != "other" || detail != "boom" {
		t.Fatalf("500: %s %q", class, detail)
	}
	if class, detail := classifyUpstreamError(502, []byte(`not json`)); class != "other" || detail != "" {
		t.Fatalf("malformed body: %s %q", class, detail)
	}
}

// The ladder reduces per-hop failures to one dominant class: a provider block
// outranks quota, an all-quota ladder reports exhaustion, anything else is a
// route failure.
func TestClassifyLadderFailureBoundaries(t *testing.T) {
	hop := func(class, detail string) *modelHop {
		h := &modelHop{modelHealth: &modelHealth{}}
		h.failWith(class, detail)
		return h
	}
	if e := classifyLadderFailure([]*modelHop{hop("quota", "spent"), hop("provider_block", "tos")}); e.class != "provider_block" || e.detail != "tos" {
		t.Fatalf("block must outrank: %+v", e)
	}
	if e := classifyLadderFailure([]*modelHop{hop("quota", "a"), hop("quota", "b")}); e.class != "quota" || e.detail != "a" {
		t.Fatalf("all-quota ladder: %+v", e)
	}
	if e := classifyLadderFailure([]*modelHop{hop("quota", "a"), hop("other", "x")}); e.class != "route_unavailable" {
		t.Fatalf("mixed ladder: %+v", e)
	}
	if e := classifyLadderFailure([]*modelHop{hop("", ""), hop("other", "x")}); e.class != "route_unavailable" {
		t.Fatalf("non-failure hop ignored: %+v", e)
	}
	if e := classifyLadderFailure(nil); e.class != "route_unavailable" {
		t.Fatalf("empty ladder: %+v", e)
	}
	e := &modelRouteError{class: "quota", detail: "spent"}
	if e.Error() != "quota: spent" {
		t.Fatalf("detail message: %q", e.Error())
	}
	if (&modelRouteError{class: "quota"}).Error() != "quota" {
		t.Fatal("bare class message")
	}
}

func TestWithinModelBudgetBoundaries(t *testing.T) {
	if withinModelBudget([]byte(`not json`)) {
		t.Fatal("non-JSON admitted")
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		body := []byte(`{"` + field + `":300000}`)
		if withinModelBudget(body) {
			t.Fatalf("%s over ceiling admitted", field)
		}
	}
	if !withinModelBudget([]byte(`{"model":"m","messages":[{"role":"u","content":"hi"}]}`)) {
		t.Fatal("ordinary request rejected")
	}
	if !withinModelBudget([]byte(`{"max_tokens":262000,"messages":[{"content":"x"}]}`)) {
		t.Fatal("reserve just under the ceiling must pass")
	}
	// At the exact ceiling the body bytes themselves tip the sum over.
	if withinModelBudget([]byte(`{"max_tokens":262144,"messages":[{"content":"x"}]}`)) {
		t.Fatal("ceiling reserve plus body bytes must exceed the bound")
	}
	if withinModelBudget([]byte(`{"messages":[{"content":"` + strings.Repeat("x", 2<<20) + `"}]}`)) {
		t.Fatal("runaway body admitted")
	}
	// A non-data image_url is charged as text, not given the image allowance.
	imageish := `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`
	if !withinModelBudget([]byte(imageish)) {
		t.Fatal("non-data image reference rejected")
	}
	broken := `{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64` + strings.Repeat("A", 200) + `,AAAA"}}]}]}`
	if !withinModelBudget([]byte(broken)) {
		t.Fatal("prefix-only data URL rejected")
	}
}

func TestRewriteModelFieldBoundaries(t *testing.T) {
	out, err := rewriteModelField([]byte(`{"model":"a","keep":1}`), "b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"model":"b"`) || !strings.Contains(string(out), `"keep":1`) {
		t.Fatalf("model not rewritten: %s", out)
	}
	raw := []byte(`not json`)
	back, err := rewriteModelField(raw, "b")
	if err != nil || string(back) != string(raw) {
		t.Fatalf("non-JSON must pass through: %v %s", err, back)
	}
}

// Hop health is a durable cooldown: failed hops stay unavailable inside the
// window, disabled hops never serve, and the state surface reports the cause.
func TestModelHopHealthBoundaries(t *testing.T) {
	h := &modelHop{modelHealth: &modelHealth{}, name: "p", base: "http://x"}
	now := time.Now()
	if !h.availableFor(now, false) || h.down(now) {
		t.Fatal("fresh hop unavailable")
	}
	h.failWith("quota", "spent")
	if !h.down(now) || h.availableFor(now, false) {
		t.Fatal("failed hop not cooling")
	}
	if class, detail := h.failure(); class != "quota" || detail != "spent" {
		t.Fatalf("failure surface: %s %q", class, detail)
	}
	state := h.state()
	if state["name"] != "p" || state["failure"] != "quota" || state["cooldown_until"] == nil {
		t.Fatalf("state surface: %v", state)
	}
	h.cool = now.Add(-time.Second)
	if h.down(now) || !h.availableFor(now, false) {
		t.Fatal("expired cooldown still blocking")
	}
	h.disabled = "parked"
	if h.availableFor(now, false) {
		t.Fatal("disabled hop served")
	}
	h.disabled = ""
	h.retrying = true
	if h.availableFor(now, false) || !h.availableFor(now, true) {
		t.Fatal("retrying hop must serve only the reserved probe")
	}
	h.retrying = false
	h.failTransport()
	if class, _ := h.failure(); class != "transport" || !h.recoverable {
		t.Fatal("transport failure must stay recoverable")
	}
}

// One probe at a time may wait out a transport cooldown; healthy ladders
// short-circuit and unreleased probes are the only reservable slot.
func TestWaitForModelRecoveryBoundaries(t *testing.T) {
	healthy := &modelHop{modelHealth: &modelHealth{}, name: "a"}
	if hop, release := waitForModelRecovery(context.Background(), []*modelHop{healthy}); hop != nil {
		release()
		t.Fatal("healthy ladder waited")
	}
	down := &modelHop{modelHealth: &modelHealth{}, name: "b"}
	down.failTransport()
	down.cool = time.Now().Add(-time.Second) // expired window, still recoverable
	ctx, cancel := context.WithCancel(context.Background())
	hop, release := waitForModelRecovery(ctx, []*modelHop{down})
	if hop != down {
		t.Fatal("recoverable hop not reserved")
	}
	down.mu.Lock()
	if !down.retrying {
		t.Fatal("probe reservation not marked")
	}
	down.mu.Unlock()
	// A second caller cannot reserve the same probe.
	if again, free := waitForModelRecovery(context.Background(), []*modelHop{down}); again != nil {
		free()
		t.Fatal("second probe reserved over the first")
	}
	cancel()
	release()
	down.mu.Lock()
	if down.retrying {
		t.Fatal("release did not clear the reservation")
	}
	down.mu.Unlock()
	// A permanently failed hop is never a recovery candidate.
	blocked := &modelHop{modelHealth: &modelHealth{}, name: "c"}
	blocked.failWith("provider_block", "tos")
	if hop, release := waitForModelRecovery(context.Background(), []*modelHop{blocked}); hop != nil {
		release()
		t.Fatal("unrecoverable hop reserved")
	}
}

func TestModelRelayLimitsBoundaries(t *testing.T) {
	hop, total, err := modelRelayLimits()
	if err != nil || hop != 5*time.Minute || total != 6*time.Minute {
		t.Fatalf("defaults: %v %v %v", hop, total, err)
	}
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "bogus")
	if _, _, err := modelRelayLimits(); err == nil {
		t.Fatal("invalid hop bound accepted")
	}
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "10m") // over the 5m cap
	if _, _, err := modelRelayLimits(); err == nil {
		t.Fatal("over-cap hop bound accepted")
	}
	t.Setenv("HUB_MODEL_RELAY_HOP_TIMEOUT", "4m")
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "3m")
	if _, _, err := modelRelayLimits(); err == nil {
		t.Fatal("hop bound above total accepted")
	}
	t.Setenv("HUB_MODEL_RELAY_TIMEOUT", "9m")
	if hop, total, err := modelRelayLimits(); err != nil || hop != 4*time.Minute || total != 9*time.Minute {
		t.Fatalf("custom bounds: %v %v %v", hop, total, err)
	}
}

// Env mode selection: a mounted registry switches the handler to the reviewed
// file contract; without it the static ladder requires an explicit model.
func TestModelRelayHandlerFromEnvModes(t *testing.T) {
	t.Setenv("HUB_MODEL_RELAY_REGISTRY", "")
	t.Setenv("HUB_MODEL_RELAY_UPSTREAMS", "")
	t.Setenv("HUB_MODEL_RELAY_MODEL", "")
	if _, err := ModelRelayHandlerFromEnv(); err == nil {
		t.Fatal("missing model must fail closed")
	}
	t.Setenv("HUB_MODEL_RELAY_MODEL", "synthetic")
	if _, err := ModelRelayHandlerFromEnv(); err != nil {
		t.Fatalf("static ladder: %v", err)
	}
	// Registry mode needs a readable models.yaml; a missing file fails closed.
	t.Setenv("HUB_MODEL_RELAY_REGISTRY", filepath.Join(t.TempDir(), "absent.yaml"))
	if _, err := ModelRelayHandlerFromEnv(); err == nil {
		t.Fatal("missing registry admitted")
	}
}

func TestRequestNeedsReasoningBoundaries(t *testing.T) {
	for _, body := range []string{`{"reasoning_effort":"high"}`, `{"reasoning":{}}`, `{"thinking":{"type":"enabled"}}`} {
		if !requestNeedsReasoning([]byte(body)) {
			t.Fatalf("reasoning request not detected: %s", body)
		}
	}
	if requestNeedsReasoning([]byte(`{"model":"m"}`)) || requestNeedsReasoning([]byte(`bogus`)) {
		t.Fatal("plain request flagged as reasoning")
	}
	if got := requestedModel([]byte(`{"model":"m1"}`)); got != "m1" {
		t.Fatalf("requestedModel: %q", got)
	}
	if got := requestedModel([]byte(`bogus`)); got != "" {
		t.Fatalf("malformed model: %q", got)
	}
}

func TestParseModelHopsBoundaries(t *testing.T) {
	for _, spec := range []string{
		"noequals",
		"A=http://x", "a=bogus", "a=ftp://x", "a=http://", "a=http://u@x",
		"a=http://x?q=1", "a=http://x#f", "a=http://x/../y",
		"a=http://x|lowercase", "a=http://x,a=http://y",
		"a=http://x,b=http://y,c=http://z,d=http://1,e=http://2,f=http://3,g=http://4,h=http://5,i=http://6",
	} {
		if _, err := parseModelHops(spec); err == nil {
			t.Errorf("spec admitted: %q", spec)
		}
	}
	hops, err := parseModelHops("a=http://x,b=http://y|VALID_ENV")
	if err != nil || len(hops) != 2 || hops[1].keyEnv != "VALID_ENV" {
		t.Fatalf("valid spec: %v %+v", err, hops)
	}
	// Unset spec defaults to the single cliproxy hop.
	hops, err = parseModelHops("")
	if err != nil || len(hops) != 1 || hops[0].name != "cliproxy" {
		t.Fatalf("default hops: %v %+v", err, hops)
	}
}

var _ = os.Getenv // keep os import used if fixture drift removes a use
