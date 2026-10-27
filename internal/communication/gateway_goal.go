package communication

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// goalRunner is the runtime surface the command needs; HTTPRunner implements
// it and tests stub it.
type goalRunner interface {
	Goal(context.Context, hubruntime.GoalRequest) (hubruntime.GoalResult, error)
}

// goalCommand implements /goal. The request rides the caller's verified
// envelope and the task's stable conversation to POST /v1/goal — the slash
// command itself is never enqueued as a run. A set persists the durable
// (principal, conversation) record and queues the kickoff as an ordinary
// spool job with the goal trigger (SPEC-0074).
func (g *Gateway) goalCommand(ctx context.Context, user User, task Task, channel string, chatID, topic, sender int64, deliveryKey, text string) error {
	reply := func(answer string) error {
		return g.queueDeliveryChannel(ctx, deliveryKey+"-reply", channel, chatID, topic, answer)
	}
	verb, arg := parseGoalArgs(text)
	answer, err := g.goalVerb(ctx, user, task, sender, channel, chatID, verb, arg)
	if err != nil {
		return err
	}
	return reply(answer)
}

// goalVerb applies one parsed goal verb and returns the user-facing reply —
// the channel command and the /v1/goals control surface share it.
func (g *Gateway) goalVerb(ctx context.Context, user User, task Task, sender int64, channel string, chatID int64, verb, arg string) (string, error) {
	reply := func(answer string) (string, error) { return answer, nil }
	lang := locale.LangOf(user.WorkspaceDir)
	if verb == "unsupported" {
		return locale.T(lang, "goal.unsupported", arg), nil
	}
	if verb == "status" {
		goal, found, err := g.spool.GoalForTask(user.ID, task.ConversationID)
		if err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		if !found {
			return reply(locale.T(lang, "goal.none"))
		}
		if goal.Engine == "native" {
			// Upstream is authoritative — refresh the mirror before answering.
			if result, rerr := g.goalRuntimeOp(ctx, user, task, sender, channel, chatID, goal, "status"); rerr == nil {
				goal = mirrorGoalState(goal, result)
				_ = g.spool.SaveGoal(goal)
			}
		}
		return reply(locale.T(lang, "goal.status", goal.Text, goal.Status, goal.Engine, goal.TurnsUsed, goal.MaxTurns))
	}
	if verb == "clear" {
		goal, found, err := g.spool.GoalForTask(user.ID, task.ConversationID)
		if err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		if !found {
			return reply(locale.T(lang, "goal.none"))
		}
		if goal.Engine == "native" {
			// Upstream holds the authoritative record — clear it there too.
			if _, rerr := g.goalRuntimeOp(ctx, user, task, sender, channel, chatID, goal, "clear"); rerr != nil {
				return reply(locale.T(lang, "goal.unavailable", rerr.Error()))
			}
		}
		if err := g.spool.ClearGoal(user.ID, task.ConversationID); err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		return reply(locale.T(lang, "goal.cleared"))
	}
	if verb == "pause" || verb == "resume" {
		goal, found, err := g.spool.GoalForTask(user.ID, task.ConversationID)
		if err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		if !found {
			return reply(locale.T(lang, "goal.none"))
		}
		prompt := "resume"
		if goal.Engine == "native" {
			// Upstream is authoritative — the verb moves its record and the
			// mirrored state (plus resume's continuation prompt) comes back.
			result, rerr := g.goalRuntimeOp(ctx, user, task, sender, channel, chatID, goal, verb)
			if rerr != nil {
				return reply(locale.T(lang, "goal.unavailable", rerr.Error()))
			}
			goal = mirrorGoalState(goal, result)
			if result.ContinuationPrompt != "" {
				prompt = result.ContinuationPrompt
			}
		} else {
			goal.Status = map[string]string{"pause": "paused", "resume": "active"}[verb]
		}
		if err := g.spool.SaveGoal(goal); err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		if verb == "pause" {
			return reply(locale.T(lang, "goal.paused"))
		}
		if err := g.enqueueGoalTurn(user, task, channel, chatID, sender, goal, prompt); err != nil {
			return reply(locale.T(lang, "goal.unavailable", err.Error()))
		}
		return reply(locale.T(lang, "goal.resumed"))
	}
	if verb != "set" {
		return reply(locale.T(lang, "goal.unhandled"))
	}
	runner, ok := g.runner.(goalRunner)
	if !ok {
		return reply(locale.T(lang, "goal.unsupported-runner"))
	}
	envelope := g.channel(channel).JobEnvelope(user, chatID, sender)
	envelope.ConversationID = task.ConversationID
	request := hubruntime.GoalRequest{ExecuteRequest: hubruntime.ExecuteRequest{
		Envelope: envelope, OrganizationID: g.config.OrganizationID, UserID: user.ID,
		ActorID: user.ID, ScopeID: g.goalScope(user, channel, chatID), Channel: channel,
		Trigger: "goal", IdempotencyKey: "goal",
	}, Op: "set", Text: arg}
	existing, found, err := g.spool.GoalForTask(user.ID, task.ConversationID)
	if err != nil {
		return reply(locale.T(lang, "goal.unavailable", err.Error()))
	}
	if found {
		request.ExpectEngine = existing.Engine
	}
	result, err := runner.Goal(ctx, request)
	if err != nil {
		return reply(locale.T(lang, "goal.unavailable", err.Error()))
	}
	if result.FailureClass != "" {
		return reply(locale.T(lang, "goal.rejected", goalFailureText(lang, result.FailureClass)))
	}
	epoch := int64(1)
	if found {
		epoch = existing.Epoch + 1
	}
	if err := g.spool.SaveGoal(Goal{
		TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID,
		Text: arg, Engine: result.Engine, Status: "active", MaxTurns: result.MaxTurns, Epoch: epoch,
	}); err != nil {
		return reply(locale.T(lang, "goal.unavailable", err.Error()))
	}
	suffix := "-e" + strconv.FormatInt(epoch, 10) + "-" + strconv.FormatInt(g.now().UnixNano(), 36)
	kickoff := Job{
		Envelope: envelope, ID: "goal-" + task.TaskID + suffix,
		OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID,
		ScopeID: g.goalScope(user, channel, chatID), Channel: channel, Trigger: "goal",
		IdempotencyKey: "goal-kickoff-" + task.TaskID + suffix,
		ChatID:         chatID, TaskID: task.TaskID, TopicID: task.TopicID,
		Text: result.KickoffPrompt, GoalEpoch: epoch, CreatedAt: g.now().UTC(),
	}
	if _, err := g.spool.Enqueue(kickoff); err != nil {
		return reply(locale.T(lang, "goal.unavailable", err.Error()))
	}
	return reply(locale.T(lang, "goal.set", result.Engine, result.MaxTurns, arg))
}

// enqueueGoalTurn queues one goal turn as an ordinary durable spool job —
// kickoff, resume and continuations all ride the same claim path.
func (g *Gateway) enqueueGoalTurn(user User, task Task, channel string, chatID, sender int64, goal Goal, prompt string) error {
	envelope := g.channel(channel).JobEnvelope(user, chatID, sender)
	envelope.ConversationID = task.ConversationID
	job := Job{
		Envelope: envelope, ID: "goal-" + task.TaskID + "-" + strconv.FormatInt(g.now().UnixNano(), 36),
		OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID,
		ScopeID: g.goalScope(user, channel, chatID), Channel: channel, Trigger: "goal",
		IdempotencyKey: "goal-" + task.TaskID + "-" + strconv.FormatInt(goal.Epoch, 10) + "-" + strconv.FormatInt(g.now().UnixNano(), 36),
		ChatID:         chatID, TaskID: task.TaskID, TopicID: task.TopicID,
		Text: prompt, GoalEpoch: goal.Epoch, CreatedAt: g.now().UTC(),
	}
	_, err := g.spool.Enqueue(job)
	return err
}

func (g *Gateway) goalScope(user User, channel string, chatID int64) string {
	if channel == "telegram_bot" && chatID < 0 {
		if group := g.groupFor(chatID); group == nil || group.Mode != "topics" {
			return "group:" + user.ID
		}
	}
	return "user:" + user.ID
}

// goalAfterJob is the goal-mode loop's post-turn hook: a completed goal turn
// evaluates the reply through the runtime, bumps the turn counter and queues
// the next turn on continue (SPEC-0074).
func (g *Gateway) goalAfterJob(ctx context.Context, job Job, response string) {
	if job.Trigger != "goal" {
		return
	}
	goal, found, err := g.spool.GoalForTask(job.UserID, taskConversation(job.ChatID, job.TaskID))
	if err != nil || !found || goal.Status != "active" || goal.Epoch != job.GoalEpoch {
		return
	}
	runner, ok := g.runner.(goalRunner)
	if !ok {
		return
	}
	result, err := runner.Goal(ctx, hubruntime.GoalRequest{ExecuteRequest: hubruntime.ExecuteRequest{
		Envelope: job.Envelope, OrganizationID: g.config.OrganizationID, UserID: job.UserID,
		ActorID: job.UserID, ScopeID: job.ScopeID, Channel: job.Channel,
		Trigger: "goal", IdempotencyKey: "goal-eval-" + job.ID,
	}, Op: "evaluate", Engine: goal.Engine, Goal: goal.Text, Response: response})
	if err != nil {
		return
	}
	goal.LastVerdict = result.Verdict
	goal.LastReason = result.Reason
	if result.TurnsUsed > 0 {
		// Native evaluations carry upstream's authoritative counters —
		// mirror them instead of keeping a second, diverging count.
		goal.TurnsUsed = result.TurnsUsed
		if result.MaxTurns > 0 {
			goal.MaxTurns = result.MaxTurns
		}
	} else if job.Trigger == "goal" {
		goal.TurnsUsed++
	}
	if result.TransportFailure {
		goal.ConsecTransport++
	} else {
		goal.ConsecTransport = 0
	}
	if goal.ConsecTransport >= 3 {
		goal.Status = "paused"
		_ = g.spool.SaveGoal(goal)
		return
	}
	if result.Verdict == "blocked" {
		goal.Status = "blocked"
		_ = g.spool.SaveGoal(goal)
		return
	}
	if result.Verdict == "done" {
		goal.Status = "done"
		_ = g.spool.SaveGoal(goal)
		suffix := "-done-" + strconv.Itoa(goal.TurnsUsed) + "-e" + strconv.FormatInt(goal.Epoch, 10) + "-" + strconv.FormatInt(g.now().UnixNano(), 36)
		_ = g.spool.EnqueueDelivery(Delivery{ID: "goal-" + job.TaskID + suffix, IdempotencyKey: "goal-" + job.TaskID + suffix, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: locale.T(locale.LangOf(g.user(job.UserID).WorkspaceDir), "goal.achieved", goal.Text, result.Reason, result.Evidence), CreatedAt: g.now().UTC()})
		return
	}
	if goal.TurnsUsed >= goal.MaxTurns {
		goal.Status = "stopped"
		_ = g.spool.SaveGoal(goal)
		return
	}
	_ = g.spool.SaveGoal(goal)
	user := g.user(job.UserID)
	task, terr := g.spool.TaskByID(user, job.ChatID, job.TaskID)
	if terr != nil {
		return
	}
	sender, _ := strconv.ParseInt(strings.TrimPrefix(job.ExternalIdentityID, "telegram-"), 10, 64)
	prompt := result.ContinuationPrompt
	if prompt == "" {
		// SPEC-0074 R3b: hub-engine evaluations carry no continuation_prompt —
		// the next turn restates the goal and the verdict reason; an empty
		// prompt would fail as "empty Hermes prompt" before any dispatch.
		prompt = "Цель: " + goal.Text + "\nВердикт предыдущего хода: " + goal.LastReason + "\nПродолжай работу над целью."
	}
	_ = g.enqueueGoalTurn(user, task, job.Channel, job.ChatID, sender, goal, prompt)
}

// goalRuntimeOp sends one control verb for an existing goal through the
// runtime — the native engine's ops land on the upstream record there.
func (g *Gateway) goalRuntimeOp(ctx context.Context, user User, task Task, sender int64, channel string, chatID int64, goal Goal, op string) (hubruntime.GoalResult, error) {
	runner, ok := g.runner.(goalRunner)
	if !ok {
		return hubruntime.GoalResult{}, errors.New("goal runner unavailable")
	}
	envelope := g.channel(channel).JobEnvelope(user, chatID, sender)
	envelope.ConversationID = task.ConversationID
	return runner.Goal(ctx, hubruntime.GoalRequest{ExecuteRequest: hubruntime.ExecuteRequest{
		Envelope: envelope, OrganizationID: g.config.OrganizationID, UserID: user.ID,
		ActorID: user.ID, ScopeID: g.goalScope(user, channel, chatID), Channel: channel,
		Trigger: "goal", IdempotencyKey: "goal-" + op,
	}, Op: op, Engine: goal.Engine})
}

// mirrorGoalState folds the runtime's authoritative fields back into the
// record after a native control op.
func mirrorGoalState(goal Goal, result hubruntime.GoalResult) Goal {
	if result.Status != "" {
		goal.Status = result.Status
	}
	if result.TurnsUsed > 0 {
		goal.TurnsUsed = result.TurnsUsed
	}
	if result.MaxTurns > 0 {
		goal.MaxTurns = result.MaxTurns
	}
	return goal
}

// goalTurnClaimable reports whether a queued goal turn still belongs to the
// live record: same epoch, still active. Anything else is stale work that
// completes without a model call.
func (g *Gateway) goalTurnClaimable(job Job) bool {
	goal, found, err := g.spool.GoalForTask(job.UserID, taskConversation(job.ChatID, job.TaskID))
	return err == nil && found && goal.Status == "active" && goal.Epoch == job.GoalEpoch
}

// pauseGoalAfterRunFailure stops the record when a goal turn's run failed for
// good: without it the goal stays "active" with nothing queued — alive in
// /goal status but never running again. Paused persists a reason and stays
// resumable through /goal resume (SPEC-0074 R4).
func (g *Gateway) pauseGoalAfterRunFailure(job Job) bool {
	goal, found, err := g.spool.GoalForTask(job.UserID, taskConversation(job.ChatID, job.TaskID))
	if err != nil || !found || goal.Status != "active" || goal.Epoch != job.GoalEpoch {
		return false
	}
	goal.Status = "paused"
	goal.LastReason = "run failed"
	return g.spool.SaveGoal(goal) == nil
}

// preemptGoalTurns bumps the record epoch on new owner work; continuations
// queued under the old epoch no-op at claim. The goal itself stays active —
// an explicit resume queues a new turn under the new epoch.
func (g *Gateway) preemptGoalTurns(principalID, conversationID string) {
	goal, found, err := g.spool.GoalForTask(principalID, conversationID)
	if err != nil || !found || goal.Status != "active" {
		return
	}
	goal.Epoch++
	_ = g.spool.SaveGoal(goal)
}

func goalFailureText(lang locale.Lang, class string) string {
	if class == "engine_conflict" {
		return locale.T(lang, "goal.err.engine-conflict")
	}
	return class
}

// handleGoals is the ToolHub-facing goal surface (SPEC-0074 R9): the same
// durable record as the channel command, task resolved from the caller's
// conversation id, principal pinned by the control bearer — never by the
// request body.
func (g *Gateway) handleGoals(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		Op             string `json:"op"`
		Text           string `json:"text,omitempty"`
		ConversationID string `json:"conversation_id"`
	}
	if decodeJSON(r, &req) != nil {
		http.Error(w, "invalid goal request", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	// Conversation ids embed their chat id (telegram-<chat>[-<task>]); the
	// home task is synthetic (never materialized) so TaskByConversation's
	// fallback needs the chat id parsed out of the conversation id.
	var chatID int64
	if rest, found := strings.CutPrefix(req.ConversationID, "telegram-"); found {
		num, _, _ := strings.Cut(rest, "-")
		chatID, _ = strconv.ParseInt(num, 10, 64)
	}
	task := g.spool.TaskByConversation(user, chatID, req.ConversationID)
	if task.ConversationID != req.ConversationID {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	var sender int64
	if len(user.TelegramIDs) > 0 {
		sender = user.TelegramIDs[0]
	}
	channel := task.Channel
	if channel == "" {
		channel = "telegram_bot"
	}
	answer, err := g.goalVerb(r.Context(), user, task, sender, channel, task.ChatID, req.Op, req.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	goal, found, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	writeJSON(w, map[string]any{
		"object": "hub.goal", "output": answer, "goal": map[string]any{
			"found": found, "text": goal.Text, "engine": goal.Engine,
			"status": goal.Status, "turns_used": goal.TurnsUsed,
			"max_turns": goal.MaxTurns,
		}})
}
