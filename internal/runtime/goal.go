package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/compaction"
)

// GoalRequest is the /v1/goal body: the trusted envelope plus one verb. The
// session is always derived from the envelope — a caller can never name
// another conversation's goal. Op is one of set|status|pause|resume|clear|
// evaluate; out-of-scope upstream verbs are refused by name on both engines
// (SPEC-0074).
type GoalRequest struct {
	ExecuteRequest
	Op     string `json:"op"`
	Text   string `json:"text,omitempty"`
	Engine string `json:"engine,omitempty"`
	// ExpectEngine refuses the request when the resolved engine differs —
	// an active goal pinned to engine A must not silently run under B.
	ExpectEngine string `json:"expect_engine,omitempty"`
	JudgeMode    string `json:"judge_mode,omitempty"`
	Goal         string `json:"goal,omitempty"`
	Response     string `json:"response,omitempty"`
}

// GoalResult is the bounded projection returned to the gateway. No
// transcript or free-form model text crosses — verdict reasons and evidence
// excerpts are capped before they leave the runtime.
type GoalResult struct {
	Engine             string `json:"engine,omitempty"`
	Judge              string `json:"judge,omitempty"`
	Verdict            string `json:"verdict,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Evidence           string `json:"evidence,omitempty"`
	Status             string `json:"status,omitempty"`
	Goal               string `json:"goal,omitempty"`
	TurnsUsed          int    `json:"turns_used,omitempty"`
	MaxTurns           int    `json:"max_turns,omitempty"`
	ShouldContinue     bool   `json:"should_continue,omitempty"`
	KickoffPrompt      string `json:"kickoff_prompt,omitempty"`
	ContinuationPrompt string `json:"continuation_prompt,omitempty"`
	Output             string `json:"output,omitempty"`
	ParseFailure       bool   `json:"parse_failure,omitempty"`
	TransportFailure   bool   `json:"transport_failure,omitempty"`
	FailureClass       string `json:"failure_class,omitempty"`
}

func (s *runtimeHTTP) goal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	defer r.Body.Close()
	var request GoalRequest
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid goal request")
		return
	}
	check := request.ExecuteRequest
	check.Text = "goal"
	if err := validateExecuteRequest(check); err != nil {
		writeRuntimeError(w, http.StatusConflict, err.Error())
		return
	}
	if request.Op == "set" {
		mc := compaction.ManagedContextFromEnv()
		engine, granted := goalEngineFor(mc, request.Engine)
		if !granted {
			writeRuntimeJSON(w, http.StatusOK, GoalResult{FailureClass: "engine_denied"})
			return
		}
		if request.ExpectEngine != "" && request.ExpectEngine != engine {
			writeRuntimeJSON(w, http.StatusOK, GoalResult{FailureClass: "engine_conflict"})
			return
		}
		if engine == "native" && goalJudgeFor(mc) == "self" {
			writeRuntimeJSON(w, http.StatusOK, GoalResult{FailureClass: "judge_denied"})
			return
		}
		maxTurns := mc.GoalsMaxTurns
		if maxTurns <= 0 {
			maxTurns = 20
		}
		result := GoalResult{Engine: engine, KickoffPrompt: request.Text, MaxTurns: maxTurns}
		if engine == "native" {
			reply, err := goalDispatch(r.Context(), sessionIDFor(request.ExecuteRequest), "set", request.Text, maxTurns)
			if err != nil {
				writeRuntimeError(w, http.StatusBadGateway, "goal dispatch failed")
				return
			}
			result.Output = reply.Output
			if reply.KickoffPrompt != "" {
				result.KickoffPrompt = reply.KickoffPrompt
			}
		}
		writeRuntimeJSON(w, http.StatusOK, result)
		return
	}
	if request.Op == "status" || request.Op == "pause" || request.Op == "resume" || request.Op == "clear" {
		mc := compaction.ManagedContextFromEnv()
		engine, granted := goalEngineFor(mc, request.Engine)
		if !granted || engine != "native" {
			writeRuntimeJSON(w, http.StatusOK, GoalResult{FailureClass: "engine_denied"})
			return
		}
		reply, err := goalDispatch(r.Context(), sessionIDFor(request.ExecuteRequest), request.Op, "", 0)
		if err != nil {
			writeRuntimeError(w, http.StatusBadGateway, "goal dispatch failed")
			return
		}
		writeRuntimeJSON(w, http.StatusOK, GoalResult{Engine: "native", Output: reply.Output,
			ContinuationPrompt: reply.ContinuationPrompt, Status: reply.Status,
			TurnsUsed: reply.TurnsUsed, MaxTurns: reply.MaxTurns})
		return
	}
	if request.Op == "evaluate" {
		mc := compaction.ManagedContextFromEnv()
		engine, granted := goalEngineFor(mc, request.Engine)
		if !granted {
			writeRuntimeJSON(w, http.StatusOK, GoalResult{FailureClass: "engine_denied"})
			return
		}
		if engine == "native" {
			eval, err := goalEvaluate(r.Context(), sessionIDFor(request.ExecuteRequest), request.Response)
			if err != nil {
				writeRuntimeError(w, http.StatusBadGateway, "goal evaluate failed")
				return
			}
			writeRuntimeJSON(w, http.StatusOK, GoalResult{
				Engine:             engine,
				Verdict:            eval.Verdict,
				Reason:             eval.Reason,
				TurnsUsed:          eval.TurnsUsed,
				MaxTurns:           eval.MaxTurns,
				ShouldContinue:     eval.ShouldContinue,
				ContinuationPrompt: eval.ContinuationPrompt,
			})
			return
		}
		judge := goalJudgeMode(mc)
		if judge == "self" {
			verdict := parseGoalMarker(request.Response)
			writeRuntimeJSON(w, http.StatusOK, GoalResult{
				Engine:       engine,
				Judge:        judge,
				Verdict:      verdict.Verdict,
				Reason:       verdict.Reason,
				ParseFailure: verdict.Parse,
			})
			return
		}
		model := os.Getenv("HUB_MANAGED_MODEL_ID")
		if judge == "aux" {
			model = mc.JudgeModel
		}
		raw, err := goalJudgeRelay(r.Context(), model, request.Goal, request.Response)
		if err != nil {
			writeRuntimeError(w, http.StatusBadGateway, "goal judge failed")
			return
		}
		verdict := parseGoalVerdict(raw)
		writeRuntimeJSON(w, http.StatusOK, GoalResult{
			Engine:           engine,
			Judge:            judge,
			Verdict:          verdict.Verdict,
			Reason:           verdict.Reason,
			Evidence:         verdict.Evidence,
			ParseFailure:     verdict.Parse,
			TransportFailure: verdict.Transport,
		})
		return
	}
	writeRuntimeError(w, http.StatusBadRequest, "unknown goal op")
}

// goalReply is the bounded projection of one upstream goal_dispatch sidecar
// reply — output, mirrored upstream state and the optional prompts only.
type goalReply struct {
	Output             string `json:"output"`
	KickoffPrompt      string `json:"kickoff_prompt"`
	ContinuationPrompt string `json:"continuation_prompt"`
	Status             string `json:"status"`
	Goal               string `json:"goal"`
	TurnsUsed          int    `json:"turns_used"`
	MaxTurns           int    `json:"max_turns"`
}

// goalEvalReply is the bounded projection of one goal_evaluate sidecar reply.
type goalEvalReply struct {
	Verdict            string `json:"verdict"`
	Reason             string `json:"reason"`
	TurnsUsed          int    `json:"turns_used"`
	MaxTurns           int    `json:"max_turns"`
	ShouldContinue     bool   `json:"should_continue"`
	ContinuationPrompt string `json:"continuation_prompt"`
}

// goalEvaluate runs upstream's post-turn goal decision on the caller's
// session via the sidecar.
func goalEvaluate(ctx context.Context, sessionID, response string) (goalEvalReply, error) {
	payload, _ := json.Marshal(map[string]any{"op": "goal_evaluate", "session_id": sessionID, "response": response})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	raw, err := compaction.Runner(ctx, payload)
	if err != nil {
		return goalEvalReply{}, err
	}
	var reply struct {
		OK bool `json:"ok"`
		goalEvalReply
	}
	if err := json.Unmarshal(raw, &reply); err != nil || !reply.OK {
		return goalEvalReply{}, errors.New("goal evaluate failed")
	}
	return reply.goalEvalReply, nil
}

// goalDispatch runs one upstream goal verb against the caller's session via
// the sidecar.
func goalDispatch(ctx context.Context, sessionID, verb, text string, maxTurns int) (goalReply, error) {
	payload, _ := json.Marshal(map[string]any{"op": "goal_dispatch", "session_id": sessionID, "verb": verb, "text": text, "max_turns": maxTurns})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := compaction.Runner(ctx, payload)
	if err != nil {
		return goalReply{}, err
	}
	var reply struct {
		OK bool `json:"ok"`
		goalReply
	}
	if err := json.Unmarshal(raw, &reply); err != nil || !reply.OK {
		return goalReply{}, errors.New("goal dispatch failed")
	}
	return reply.goalReply, nil
}

// goalEngineFor resolves the goals engine: the declared goals.engine (auto ->
// hub) unless the caller pinned a concrete engine. A declared engine pins it;
// undeclared admits both (SPEC-0074).
func goalEngineFor(mc *compaction.ManagedContext, requested string) (string, bool) {
	engine := requested
	if engine == "" || engine == "auto" {
		engine = mc.GoalsEngine
		if engine == "" || engine == "auto" {
			engine = "hub"
		}
	}
	if engine != "hub" && engine != "native" {
		return "", false
	}
	if (mc.GoalsEngine == "hub" || mc.GoalsEngine == "native") && engine != mc.GoalsEngine {
		return "", false
	}
	return engine, true
}

func goalJudgeFor(mc *compaction.ManagedContext) string {
	return mc.GoalsJudge
}

// goalJudgeMode resolves the concrete judge from the declared goals_judge.
func goalJudgeMode(mc *compaction.ManagedContext) string {
	if mc.GoalsJudge == "aux" || mc.GoalsJudge == "self" {
		return mc.GoalsJudge
	}
	return "main"
}

// parseGoalMarker reads the self judge's in-band verdict — the last
// GOAL_STATUS: line in the response. A missing or malformed marker counts
// toward the parse-failure streak.
func parseGoalMarker(response string) goalVerdict {
	verdict := ""
	reason := ""
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "GOAL_STATUS:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "GOAL_STATUS:"))
		word, remainder, _ := strings.Cut(rest, " ")
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "done" || word == "blocked" || word == "continue" {
			verdict = word
			reason = strings.TrimSpace(strings.TrimPrefix(remainder, "—"))
		} else {
			verdict = ""
		}
	}
	if verdict == "" {
		return goalVerdict{Verdict: "continue", Reason: "missing or malformed GOAL_STATUS marker", Parse: true}
	}
	return goalVerdict{Verdict: verdict, Reason: reason}
}
