package communication

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/identity"
)

// tickProactive is the proactive pass of the control loop: schedule -> due
// occurrence -> dispatch -> deterministic probing for watcher/check kinds.
func (g *Gateway) tickProactive(now time.Time) { g.tickProactiveContext(context.Background(), now) }

func (g *Gateway) tickProactiveContext(ctx context.Context, now time.Time) {
	steps := []func() error{
		func() error { return g.spool.TickSchedules(now, g.config.NativeCron, g.authorizeOccurrence) },
		func() error { return g.spool.DispatchDueOccurrences(now, g.authorizeOccurrence) },
		func() error { return g.spool.RunDueScripts(ctx, now, g.authorizeOccurrence, g.runIsolatedScript) },
		func() error {
			return g.spool.ProbeDueOccurrencesContext(ctx, now, g.authorizeOccurrence, g.fetchWatch, g.toolCallProbe, g.serviceProbe)
		},
		func() error { return g.spool.DispatchDueOccurrences(now, g.authorizeOccurrence) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			if ctx.Err() == nil {
				cause := strings.Join(strings.Fields(diagnostics.Redact(err.Error())), " ")
				if len(cause) > 256 {
					cause = cause[:256]
				}
				log.Printf("routine tick failed stage=%d: %s; work remains pending", i, cause)
			}
		}
	}
}

func (g *Gateway) authorizeOccurrence(job Job) error {
	user := g.user(job.UserID)
	configured := user.ID != ""
	if user.ID == "" {
		if mapped, ok := g.users[job.ChatID]; ok {
			user = mapped
		}
	}
	if !user.Enabled || user.ID != job.UserID || job.ActorID != user.ID || job.ScopeID != "user:"+user.ID || job.OrganizationID != g.config.OrganizationID || job.Trigger != "cron" {
		return errors.New("routine owner unavailable")
	}
	ch := g.channel(job.Channel)
	if ch == nil {
		return errors.New("routine owner unavailable")
	}
	return ch.AuthorizeOccurrence(job, user, configured)
}

func (g *Gateway) ownsRoutineEnvelope(job Job, canonical identity.Envelope) bool {
	if job.TaskID != "" && job.TaskID != defaultTaskID {
		// The record is atomically replaced; this read also runs under the spool
		// lock during dispatch, so taking that lock here would deadlock.
		task, err := g.spool.loadTaskLocked(job.PrincipalID, job.TaskID)
		if err != nil || task.Archived || task.ContextID != job.ContextID || task.ChatID != job.ChatID || task.TopicID != job.TopicID {
			return false
		}
		if task.TopicID != 0 {
			bound, err := g.spool.topicTaskLocked(job.PrincipalID, job.ChatID, job.TopicID)
			if err != nil || bound != job.TaskID {
				return false
			}
		}
	}
	if job.ConversationID != canonical.ConversationID {
		// Before admission the envelope is canonical; dispatch pins the
		// schedule-derived conversation. In-flight occurrences admitted before
		// that change still carry the owned task conversation.
		if job.ConversationID != g.routineConversationOf(job.ID) &&
			(job.TaskID == "" || job.TaskID == defaultTaskID || job.ConversationID != taskConversation(job.ChatID, job.TaskID)) {
			return false
		}
		canonical.ConversationID = job.ConversationID
	}
	return job.Envelope == canonical
}

// routineConversationOf resolves the schedule-pinned conversation for a
// routine-produced job: occurrence-shaped ids go through the durable
// occurrence record, stream batch jobs through the schedule id they carry.
func (g *Gateway) routineConversationOf(jobID string) string {
	if o, ok := g.occurrenceFor(jobID); ok {
		return routineConversation(o.ScheduleID)
	}
	if sid := streamRefScheduleID(jobID); sid != "" {
		return routineConversation(sid)
	}
	return ""
}

func (g *Gateway) occurrenceFor(jobID string) (RoutineOccurrence, bool) {
	body, err := os.ReadFile(filepath.Join(g.spool.root, "occurrences", spoolFileID(jobID)+".json"))
	var o RoutineOccurrence
	if err != nil || json.Unmarshal(body, &o) != nil {
		return RoutineOccurrence{}, false
	}
	return o, true
}

// streamRefScheduleID recovers the schedule id a stream fan-out ref was
// deterministically seeded from (streams.go builds them where no occurrence
// exists): stream-<schedule id>[-<probe revision>]-<16-hex batch hash>.
func streamRefScheduleID(id string) string {
	rest, ok := strings.CutPrefix(id, "stream-")
	if !ok {
		return ""
	}
	i := strings.LastIndexByte(rest, '-')
	if i <= 0 || len(rest[i+1:]) != 16 {
		return ""
	}
	if _, err := hex.DecodeString(rest[i+1:]); err != nil {
		return ""
	}
	sid := rest[:i]
	if j := strings.LastIndexByte(sid, '-'); j > 0 {
		if _, err := strconv.ParseUint(sid[j+1:], 10, 64); err == nil {
			sid = sid[:j]
		}
	}
	if !commodel.IDPattern.MatchString(sid) {
		return ""
	}
	return sid
}

// occurrenceRefAuthorizedLocked re-authorizes a routine-produced delivery
// ref at send time: occurrence-shaped refs re-check the live occurrence;
// stream batch refs re-check the owning schedule stays stream-active and
// authorized. s.mu must be held.
func (g *Gateway) occurrenceRefAuthorizedLocked(id string) bool {
	if o, ok := g.occurrenceFor(id); ok {
		return g.spool.occurrenceCurrentLocked(o) && g.authorizeOccurrence(o.Job) == nil
	}
	sid := streamRefScheduleID(id)
	if sid == "" {
		return false
	}
	schedule, err := g.spool.loadScheduleLocked(sid)
	if err != nil || !streamActive(&schedule, g.now().UTC()) {
		return false
	}
	job := Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, SlackChannel: schedule.SlackChannel, SlackThread: schedule.SlackThread, Trigger: "cron"}
	return g.authorizeOccurrence(job) == nil
}
