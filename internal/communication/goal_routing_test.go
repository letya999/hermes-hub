package communication

import (
	"context"
	"testing"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestGoalAfterJobIgnoresUnrelatedTurns(t *testing.T) {
	for _, trigger := range []string{"message", "cron", "goal"} {
		t.Run(trigger, func(t *testing.T) {
			g := groupGateway(t, testConfig(t))
			runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "blocked"}}
			g.runner = runner
			task, err := g.spool.ResolveTask(g.user("alice"), 11, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			goal := Goal{PrincipalID: "alice", TaskID: task.TaskID, ConversationID: task.ConversationID, Engine: "hub", Text: "work", Status: "active", Epoch: 2, MaxTurns: 5}
			if err := g.spool.SaveGoal(goal); err != nil {
				t.Fatal(err)
			}
			before, _, err := g.spool.GoalForTask("alice", task.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			epoch := int64(2)
			if trigger == "goal" {
				epoch = 1
			}
			g.goalAfterJob(context.Background(), Job{UserID: "alice", ChatID: 11, TaskID: task.TaskID, Trigger: trigger, GoalEpoch: epoch}, "clarifying question")
			after, _, err := g.spool.GoalForTask("alice", task.ConversationID)
			if err != nil || after != before || len(runner.evals) != 0 {
				t.Fatalf("unrelated turn changed goal: before=%+v after=%+v evaluations=%d err=%v", before, after, len(runner.evals), err)
			}
		})
	}
}

func TestGoalRuntimeRequestsKeepGroupScope(t *testing.T) {
	g := groupGateway(t, groupConfig(t))
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Engine: "native", MaxTurns: 5, KickoffPrompt: "work", Verdict: "blocked"}}
	g.runner = runner
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, -100123, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, verb := range []string{"set", "status", "pause", "resume", "evaluate", "clear"} {
		if verb == "evaluate" {
			job, err := g.spool.ClaimJob()
			if err != nil || job == nil {
				t.Fatalf("claim: %v", err)
			}
			g.goalAfterJob(ctx, *job, "progress")
		} else if _, err := g.goalVerb(ctx, user, task, 11, "telegram_bot", -100123, verb, "work"); err != nil {
			t.Fatal(err)
		}
		if len(runner.evals) == 0 {
			t.Fatalf("%s did not reach runtime", verb)
		}
		req := runner.evals[len(runner.evals)-1]
		if req.Op != verb || req.ScopeID != "group:alice" || req.DeliveryTargetID != "telegram--100123" || req.ExternalIdentityID != "telegram-11" || req.ConversationID != task.ConversationID {
			t.Errorf("%s lost runtime authority: %+v", verb, req.ExecuteRequest)
		}
	}
}

func TestGoalTurnsKeepChannelAuthorization(t *testing.T) {
	for _, mode := range []string{"private", "topics", "mention"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig(t)
			chatID, topic, target, scope := int64(11), int64(0), "telegram-11", "user:alice"
			if mode == "topics" {
				c = topicsConfig(t)
				chatID, topic, target = -100777, 42, "telegram--100777"
			} else if mode == "mention" {
				c = groupConfig(t)
				chatID, target, scope = -100123, "telegram--100123", "group:alice"
			}
			g := groupGateway(t, c)
			runner := &goalLoopRunner{result: hubruntime.GoalResult{Engine: "hub", MaxTurns: 5, KickoffPrompt: "work"}}
			g.runner = runner
			user := g.user("alice")
			task, err := g.spool.ResolveTask(user, chatID, topic, true)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := g.goalVerb(ctx, user, task, 11, "telegram_bot", chatID, "set", "finish task"); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"kickoff", "continuation", "resume"} {
				job, err := g.spool.ClaimJob()
				if err != nil || job == nil {
					t.Fatalf("%s missing job: %v", phase, err)
				}
				if job.DeliveryTargetID != target || job.ScopeID != scope || job.ExternalIdentityID != "telegram-11" || job.ConversationID != task.ConversationID || job.TopicID != topic {
					t.Fatalf("%s lost routing: %+v scope=%s topic=%d", phase, job.Envelope, job.ScopeID, job.TopicID)
				}
				if err := g.authorizeGroupMapping(mappingFromJob(*job, g.now())); err != nil {
					t.Fatalf("%s rejected: %v", phase, err)
				}
				if err := g.spool.CompleteJob(job.ID); err != nil {
					t.Fatal(err)
				}
				if phase == "kickoff" {
					runner.result = hubruntime.GoalResult{Verdict: "continue", ContinuationPrompt: "next"}
					g.goalAfterJob(ctx, *job, "progress")
				} else if phase == "continuation" {
					for _, verb := range []string{"pause", "resume"} {
						if _, err := g.goalVerb(ctx, user, task, 11, "telegram_bot", chatID, verb, ""); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		})
	}
}
