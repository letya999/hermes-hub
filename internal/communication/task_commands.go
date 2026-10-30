package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// newTaskCommand implements /new [имя]: create a session task and switch to
// it. In a group the bot first tries to mint a dedicated forum topic for the
// session; when the group is not a forum or the bot lacks manage_topics the
// command falls back to binding the topic it was typed in, as before. The
// reply lands in the topic the session ended up bound to.
// Tasks are owner-scoped by construction: the resolved user is the only
// authority.
func (g *Gateway) newTaskCommand(ctx context.Context, user User, chatID, topic int64, text string) (string, int64) {
	lang := locale.LangOf(user.WorkspaceDir)
	fields := strings.Fields(text)
	name := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	bind := topic
	if chatID < 0 {
		bind = 0
	}
	created, isNew, err := g.spool.CreateTask(user, chatID, bind, name, true)
	if err != nil {
		return locale.T(lang, "task.rejected", err.Error()), topic
	}
	if !isNew {
		return locale.T(lang, "task.exists", taskLabel(lang, created)), topic
	}
	replyTopic, freshTopic := topic, false
	if chatID < 0 {
		if forumTopic, err := g.api.CreateForumTopic(ctx, chatID, created.Name); err == nil {
			if err := g.spool.RebindTaskTopic(user, chatID, created.TaskID, forumTopic); err == nil {
				created.TopicID, replyTopic, freshTopic = forumTopic, forumTopic, true
			}
		}
		if !freshTopic && topic != 0 {
			if err := g.spool.RebindTaskTopic(user, chatID, created.TaskID, topic); err != nil {
				return locale.T(lang, "task.created", taskLabel(lang, created)) + " " + locale.T(lang, "task.topic-bind-failed", err.Error()), topic
			}
			created.TopicID = topic
		}
	}
	answer := locale.T(lang, "task.created", taskLabel(lang, created))
	if created.NameAuto {
		answer += " " + locale.T(lang, "task.autoname")
	}
	if freshTopic {
		answer += " " + locale.T(lang, "task.topic-created")
	} else if created.TopicID != 0 {
		answer += " " + locale.T(lang, "task.topic-bound")
	}
	return answer, replyTopic
}

// useCommand implements /use <id|имя|default>: switch the chat's current
// session. Posting afterwards follows the selected task's conversation.
func (g *Gateway) useCommand(ctx context.Context, user User, chatID int64, text string) string {
	lang := locale.LangOf(user.WorkspaceDir)
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return locale.T(lang, "task.use-usage")
	}
	selector := strings.Join(fields[1:], " ")
	selected, err := g.spool.UseTask(user, chatID, selector)
	if err == nil {
		return locale.T(lang, "task.current", taskLabel(lang, selected))
	}
	restored, rebound, uerr := g.spool.UnarchiveTask(user, chatID, selector)
	if uerr != nil {
		return locale.T(lang, "task.not-found", err.Error())
	}
	if rebound && chatID < 0 {
		if err := g.api.ReopenForumTopic(ctx, chatID, restored.TopicID); err != nil {
			log.Printf("gateway forum topic reopen failed chat=%d topic=%d: %v", chatID, restored.TopicID, err)
		}
	}
	return locale.T(lang, "task.restored", taskLabel(lang, restored))
}

// deleteCommand implements /delete [id|имя]: bare removes the current task,
// a selector removes the named one. In a forum group the session's topic is
// deleted with it; a confirmation typed inside that deleted topic is
// delivered to the group root instead. The upstream Hermes session is kept
// for audit; only the registry record and routing are removed.
func (g *Gateway) deleteCommand(ctx context.Context, user User, chatID, topic int64, task Task, text string) (string, int64) {
	lang := locale.LangOf(user.WorkspaceDir)
	fields := strings.Fields(text)
	selector := task.TaskID
	if len(fields) >= 2 {
		selector = strings.Join(fields[1:], " ")
	}
	deleted, err := g.spool.DeleteTaskBySelector(user, chatID, selector)
	if err != nil {
		return locale.T(lang, "task.delete-failed", err.Error()), topic
	}
	replyTopic := topic
	if deleted.TopicID != 0 && chatID < 0 {
		if err := g.api.DeleteForumTopic(ctx, chatID, deleted.TopicID); err != nil {
			log.Printf("gateway forum topic delete failed chat=%d topic=%d: %v", chatID, deleted.TopicID, err)
		}
		if replyTopic == deleted.TopicID {
			replyTopic = 0
		}
	}
	return locale.T(lang, "task.deleted", taskLabel(lang, deleted)), replyTopic
}

// archiveCommand implements /archive [id|имя]: retire the resolved or
// addressed session. In a forum group its topic is closed so the UI mirrors
// the archive state; the Hermes session itself is kept for audit and /use
// can restore it.
func (g *Gateway) archiveCommand(ctx context.Context, user User, chatID int64, task Task, text string) string {
	lang := locale.LangOf(user.WorkspaceDir)
	if fields := strings.Fields(text); len(fields) >= 2 {
		addressed, err := g.spool.TaskBySelector(user, chatID, strings.Join(fields[1:], " "))
		if err != nil {
			return locale.T(lang, "task.not-found", err.Error())
		}
		task = addressed
	}
	archived, err := g.spool.ArchiveTask(user, chatID, task.TaskID)
	if err != nil {
		return locale.T(lang, "task.archive-failed", err.Error())
	}
	if archived.TopicID != 0 && chatID < 0 {
		if err := g.api.CloseForumTopic(ctx, chatID, archived.TopicID); err != nil {
			log.Printf("gateway forum topic close failed chat=%d topic=%d: %v", chatID, archived.TopicID, err)
		}
	}
	return locale.T(lang, "task.archived", taskLabel(lang, archived), archived.UpdatedAt.UTC().Format("02.01.2006 15:04"), archived.TaskID)
}

// archivedCommand implements /archived: list archived sessions with dates.
func (g *Gateway) archivedCommand(user User, chatID int64) string {
	lang := locale.LangOf(user.WorkspaceDir)
	tasks, err := g.spool.ListArchivedTasks(user, chatID)
	if err != nil {
		return locale.T(lang, "task.archive-list-failed")
	}
	if len(tasks) == 0 {
		return locale.T(lang, "task.archive-empty")
	}
	lines := []string{locale.T(lang, "task.archive-title")}
	for _, task := range tasks {
		line := "— " + taskLabel(lang, task)
		if !task.UpdatedAt.IsZero() {
			line += locale.T(lang, "task.archived-at", task.UpdatedAt.UTC().Format("02.01.2006 15:04"))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// stopCommand implements /stop: cancel the non-terminal job bound to the
// resolved session. A queued job is cancelled locally; an admitted run still
// goes through the runtime control loop for confirmation.
func (g *Gateway) stopCommand(user User, task Task, callerForJob func(string) identity.Envelope) string {
	lang := locale.LangOf(user.WorkspaceDir)
	jobs, err := g.spool.ActiveJobsForTask(user.ID, task.TaskID)
	if err != nil {
		return locale.T(lang, "stop.check-failed")
	}
	if len(jobs) == 0 {
		return locale.T(locale.LangOf(user.WorkspaceDir), "stop.none")
	}
	lines := []string{}
	for _, job := range jobs {
		updated, err := g.spool.RequestCancel(job.JobID, callerForJob(job.JobID))
		if err != nil {
			lines = append(lines, locale.T(lang, "stop.cancel-unavailable", job.JobID))
			continue
		}
		lines = append(lines, locale.T(lang, "stop.stopping", job.JobID, updated.Status))
	}
	return strings.Join(lines, "\n")
}

// forumTopicCreated mirrors a user-created forum topic into a bound session
// named exactly like it: the first post lands in that session instead of
// minting a "topic-<id>" placeholder. The chat's current pointer stays
// untouched until the user actually posts. A topic bound before the service
// message arrived gets its placeholder replaced by the real name.
func (g *Gateway) forumTopicCreated(user User, chatID, topicID int64, name string) error {
	if topicID == 0 {
		return nil
	}
	name = strings.TrimSpace(name)
	if _, isNew, err := g.spool.CreateTask(user, chatID, topicID, name, false); err == nil && isNew {
		return nil
	}
	if err := g.spool.FixTopicPlaceholder(user, chatID, topicID, name); err != nil {
		log.Printf("gateway forum topic bind failed chat=%d topic=%d: %v", chatID, topicID, err)
	}
	return nil
}

func (g *Gateway) taskStatus(user User, task Task) string {
	lang := locale.LangOf(user.WorkspaceDir)
	answer := locale.T(lang, "task.current", taskLabel(lang, task))
	if !task.CreatedAt.IsZero() {
		answer += " " + locale.T(lang, "task.created-at", task.CreatedAt.UTC().Format("02.01.2006 15:04"))
	}
	if task.NameAuto {
		answer += " " + locale.T(lang, "task.autoname-later")
	}
	if task.TopicID != 0 {
		answer += " " + locale.T(lang, "task.topic-ref", itoa64(task.TopicID))
	}
	mapping, found, err := g.spool.SessionFor(user.ID, user.ID, task.ConversationID)
	if err == nil && found && mapping.SessionID != "" {
		answer += " " + locale.T(lang, "task.session-ref", mapping.SessionID)
	}
	return answer + g.surfaceLines(user) + g.healthLines(user)
}

// runtimeSurface is the additive health observation the runtime reports and
// the supervisor persists.
type runtimeSurface struct {
	PrincipalID  string            `json:"principal_id"`
	ContextID    string            `json:"context_id"`
	ToolSurface  string            `json:"tool_surface"`
	MissingTools []string          `json:"missing_tools"`
	ToolReasons  map[string]string `json:"tool_reasons"`
	ModelStatus  string            `json:"model_status"`
	Restarts     int               `json:"restarts"`
}

// surfaceLines renders the caller's own tool-surface and model observations in
// plain language (SPEC-0053 R1.4-R1.6). The supervised gateway reads the
// persisted supervisor records and keeps strictly to the caller's
// principal/context — a sibling's missing tools never leak. An unreachable
// supervisor adds nothing: status stays available when supervision is not.
func (g *Gateway) surfaceLines(user User) string {
	lang := locale.LangOf(user.WorkspaceDir)
	if strings.TrimSpace(g.config.RuntimeURL) == "" || strings.TrimSpace(g.config.RuntimeAuth) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var surface *runtimeSurface
	if g.config.Supervised {
		surface = g.supervisedSurface(ctx, user)
	} else {
		surface = g.residentSurface(ctx)
	}
	if surface == nil {
		return ""
	}
	var lines []string
	if len(surface.MissingTools) > 0 {
		lines = append(lines, "", locale.T(lang, "surface.missing-title"))
		for _, name := range surface.MissingTools {
			lines = append(lines, locale.T(lang, "surface.missing-tool", name))
		}
	}
	keys := make([]string, 0, len(surface.ToolReasons))
	for key := range surface.ToolReasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, "- "+key+": "+surface.ToolReasons[key]+".")
	}
	if surface.ModelStatus == "model_not_found" {
		lines = append(lines, locale.T(lang, "surface.model-not-found"))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n" + strings.Join(lines, "\n")
}

func (g *Gateway) supervisedSurface(ctx context.Context, user User) *runtimeSurface {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/runtimes", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var runtimes []runtimeSurface
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&runtimes) != nil {
		return nil
	}
	for i := range runtimes {
		if runtimes[i].PrincipalID == user.ID && runtimes[i].ContextID == user.ID {
			return &runtimes[i]
		}
	}
	return nil
}

// residentSurface reads the resident runtime's own health when there is no
// supervisor — the same additive fields, and the resident runtime is the
// caller's by construction.
func (g *Gateway) residentSurface(ctx context.Context) *runtimeSurface {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/health", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var surface runtimeSurface
	if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&surface) != nil {
		return nil
	}
	return &surface
}

// sessionsCommand implements /sessions: the owner's live tasks in this chat
// with the current one marked. Archived tasks stay in the spool for audit but
// are not listed.
func (g *Gateway) sessionsCommand(user User, chatID int64) string {
	lang := locale.LangOf(user.WorkspaceDir)
	tasks, err := g.spool.ListTasks(user, chatID)
	if err != nil {
		return locale.T(lang, "task.list-failed")
	}
	current, resolveErr := g.spool.ResolveTask(user, chatID, 0, false)
	if resolveErr != nil {
		return locale.T(lang, "task.list-failed")
	}
	lines := []string{locale.T(lang, "task.sessions-title")}
	for _, task := range tasks {
		line := "- " + taskLabel(lang, task)
		if !task.CreatedAt.IsZero() {
			line += " — " + task.CreatedAt.UTC().Format("02.01 15:04")
		}
		if task.TaskID == current.TaskID {
			line += " — " + locale.T(lang, "task.sessions-current")
		}
		if task.TopicID != 0 {
			line += locale.T(lang, "task.sessions-topic", itoa64(task.TopicID))
		}
		lines = append(lines, line)
	}
	lines = append(lines, locale.T(lang, "task.sessions-hint"))
	return strings.Join(lines, "\n")
}

// usageCommand implements /usage: a compact report — tokens burned, context
// window fill, compactions, and subscription limits with reset times. Only
// measured upstream fields are rendered; missing counters print "unknown"
// instead of an estimate.
func (g *Gateway) usageCommand(ctx context.Context, user User, sender int64, task Task) string {
	lang := locale.LangOf(user.WorkspaceDir)
	runner, ok := g.runner.(interface {
		SessionUsage(context.Context, hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error)
	})
	if !ok {
		return locale.T(lang, "usage.unsupported")
	}
	envelope := user.Envelope(sender)
	envelope.ConversationID = task.ConversationID
	request := hubruntime.ExecuteRequest{
		Envelope:       envelope,
		OrganizationID: g.config.OrganizationID,
		UserID:         user.ID,
		ActorID:        user.ID,
		ScopeID:        "user:" + user.ID,
		Channel:        "telegram_bot",
		Trigger:        "usage",
		IdempotencyKey: "usage",
	}
	report, err := runner.SessionUsage(ctx, request)
	if err != nil && usageTransient(err) {
		// The supervisor ensures the runtime before measuring; one retry
		// covers the window where it dies between acquire and forward.
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
		report, err = runner.SessionUsage(ctx, request)
	}
	if err != nil {
		if usageTransient(err) {
			return locale.T(lang, "usage.transient")
		}
		return locale.T(lang, "usage.error", err.Error())
	}
	if !report.SessionFound {
		return locale.T(lang, "usage.no-session", taskLabel(lang, task))
	}
	lines := []string{locale.T(lang, "usage.session-line", usageNum(lang, report.InputTokens), usageNum(lang, report.OutputTokens), usageNum(lang, report.CacheReadTokens), usageNum(lang, report.CacheWriteTokens))}
	// Observed compactions = measured lineage rotations plus journaled
	// in-place events; either side unknown leaves the whole count unknown.
	var observed *int64
	if report.Compactions != nil || report.InPlaceCompactions != nil {
		total := int64(0)
		if report.Compactions != nil {
			total += *report.Compactions
		}
		if report.InPlaceCompactions != nil {
			total += *report.InPlaceCompactions
		}
		observed = &total
	}
	compactions := usageNum(lang, observed)
	if report.CompactionsTruncated && report.Compactions != nil {
		// Hop-capped lineage is a lower bound, never a silent truncation.
		compactions = "≥" + compactions
	}
	lastEvent := ""
	if report.LastCompaction != nil {
		lastEvent = " (" + report.LastCompaction.Trigger + ")"
	}
	contextLine := locale.T(lang, "usage.compactions", compactions, lastEvent, usageSuffix(lang, report.LastCompactionAt))
	if report.ContextTokens != nil && report.ContextWindow != nil && *report.ContextWindow > 0 {
		contextLine = locale.T(lang, "usage.context-line", strconv.FormatInt(*report.ContextTokens, 10), strconv.FormatInt(*report.ContextWindow, 10), strconv.FormatInt(*report.ContextTokens*100/(*report.ContextWindow), 10)) + " " + contextLine
	}
	lines = append(lines, contextLine)
	if totals, err := g.spool.UsageTotals("user:" + user.ID); err == nil && totals.Jobs > 0 {
		lines = append(lines, locale.T(lang, "usage.totals-line", totals.Jobs, totals.InputTokens, totals.OutputTokens))
	}
	lines = append(lines, g.quotaLines(ctx, lang)...)
	return strings.Join(lines, "\n")
}

func (g *Gateway) journalUsage(user User, command string) (string, bool) {
	lang := locale.LangOf(user.WorkspaceDir)
	fields := strings.Fields(command)
	var since time.Time
	model := ""
	for _, field := range fields[1:] {
		days := map[string]int{"day": 1, "week": 7, "month": 30}[field]
		if days > 0 && since.IsZero() {
			since = g.now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
			continue
		}
		if model == "" && (strings.HasPrefix(field, "model=") || !since.IsZero()) && days == 0 {
			model = strings.TrimPrefix(field, "model=")
			if model != "" && len(model) <= 128 {
				continue
			}
		}
		return "", false
	}
	totals, err := g.spool.UsageTotalsSince("user:"+user.ID, since, model)
	if err != nil {
		return locale.T(lang, "usage.journal-failed"), true
	}
	return locale.T(lang, "usage.journal-totals", totals.Jobs, totals.InputTokens, totals.OutputTokens, totals.UnknownJobs, totals.LegacyJobs, totals.InitialSnapshots), true
}

// usageTransient reports lookup-path failures worth one retry: the runtime is
// mid-spawn (404/503) or died between acquire and forward (502).
func usageTransient(err error) bool {
	return strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "503")
}

func usageNum(lang locale.Lang, v *int64) string {
	if v == nil {
		return locale.T(lang, "usage.unknown")
	}
	return strconv.FormatInt(*v, 10)
}

func usageSuffix(lang locale.Lang, at string) string {
	if at == "" {
		return ""
	}
	return locale.T(lang, "usage.last-suffix", at)
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// modelCommand implements /model: list the caller's granted logical models
// and switch the selection through the caller's own model-relay admin
// endpoint. A single-owner gateway keeps the HUB_MODEL_RELAY_URL env path; a
// shared gateway resolves principal → {relay URL, admin bearer} from the
// enrolled model-relay-admins.json. The relay re-checks grants, so the
// command can never widen access; an unmapped principal gets no relay at all.
func modelCommand(ctx context.Context, user User, text string) string {
	lang := locale.LangOf(user.WorkspaceDir)
	base, admin := modelRelayFor(user.ID)
	if base == "" {
		return locale.T(lang, "model.no-relay")
	}
	call := func(ctx context.Context, method, path string, body []byte) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body)) // #nosec G704 -- internal control endpoint
		if err != nil {
			return 0, nil
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if admin != "" {
			req.Header.Set("Authorization", "Bearer "+admin)
		}
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req) // #nosec G704 -- internal control endpoint
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b
	}
	fields := strings.Fields(text)
	if len(fields) < 2 {
		code, body := call(ctx, http.MethodGet, "/v1/models", nil)
		if code != http.StatusOK {
			return locale.T(lang, "model.list-failed")
		}
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &catalog) != nil || len(catalog.Data) == 0 {
			return locale.T(lang, "model.list-empty")
		}
		var names []string
		for _, m := range catalog.Data {
			names = append(names, m.ID)
		}
		var current modelSelection
		if scode, sbody := call(ctx, http.MethodGet, "/v1/model", nil); scode == http.StatusOK {
			_ = json.Unmarshal(sbody, &current)
		}
		if view := modelAccountView(ctx, call, lang, current); view != "" {
			return view
		}
		currentName := ""
		if current.Model != "" {
			currentName = locale.T(lang, "model.current-suffix", current.Model)
		}
		return locale.T(lang, "model.list-line", strings.Join(names, ", "), currentName)
	}
	name := fields[1]
	payload, _ := json.Marshal(map[string]string{"model": name})
	code, _ := call(ctx, http.MethodPut, "/v1/model", payload)
	switch code {
	case http.StatusOK:
		return locale.T(lang, "model.switched", name)
	case http.StatusForbidden:
		return locale.T(lang, "model.denied", name)
	default:
		return locale.T(lang, "model.switch-failed")
	}
}

// modelRelayFor resolves which relay a /model caller owns. The env pair is
// the reviewed single-space contract; the admins file is the shared-gateway
// path the stack renders for managed siblings. The file is read per call so
// an in-place enrollment update is picked up without a gateway restart.
func modelRelayFor(principal string) (base, admin string) {
	if env := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_URL")); env != "" {
		return env, strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMIN"))
	}
	path := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_ADMINS_FILE"))
	if path == "" || principal == "" {
		return "", ""
	}
	body, err := os.ReadFile(path) // #nosec G703 -- HUB_MODEL_RELAY_ADMINS_FILE is operator-rendered service config, not request input
	if err != nil {
		return "", ""
	}
	var entries map[string]struct {
		URL   string `json:"url"`
		Admin string `json:"admin"`
	}
	if json.Unmarshal(body, &entries) != nil {
		return "", ""
	}
	entry, ok := entries[principal]
	if !ok || strings.TrimSpace(entry.URL) == "" {
		return "", ""
	}
	return strings.TrimSpace(entry.URL), strings.TrimSpace(entry.Admin)
}

// modelSelection mirrors the relay's /v1/model answer: a logical name, or a
// provider-side catalog id pinned to the account that serves it.
type modelSelection struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
}

// modelAccountView renders /model's account view from the relay's /v1/registry.
// Every printed name is directly usable as a /model argument: logical rows by
// their registry name, account extras as provider/model. The relay's switch
// grant steers the footer and the extras — below catalog level the account
// catalog is not advertised because those ids cannot be selected. Empty when
// the relay predates the endpoint; the caller then falls back to the plain
// catalog.
func modelAccountView(ctx context.Context, call func(context.Context, string, string, []byte) (int, []byte), lang locale.Lang, current modelSelection) string {
	code, body := call(ctx, http.MethodGet, "/v1/registry", nil)
	if code != http.StatusOK {
		return ""
	}
	var registry struct {
		Switch string `json:"switch"`
		Models []struct {
			Name   string `json:"name"`
			Ladder []struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
				Down     bool   `json:"down,omitempty"`
			} `json:"ladder"`
		} `json:"models"`
		Accounts []struct {
			Provider string   `json:"provider"`
			Down     bool     `json:"down,omitempty"`
			Catalog  []string `json:"catalog,omitempty"`
		} `json:"accounts"`
	}
	if json.Unmarshal(body, &registry) != nil || len(registry.Models) == 0 {
		return ""
	}
	if registry.Switch == "" {
		registry.Switch = "logical"
	}
	footer := locale.T(lang, "model.footer-name")
	switch registry.Switch {
	case "catalog":
		footer = locale.T(lang, "model.footer-account")
	case "none":
		footer = locale.T(lang, "model.switching-off")
	}
	var out strings.Builder
	if len(registry.Accounts) > 0 {
		out.WriteString(locale.T(lang, "model.accounts-title"))
		for _, a := range registry.Accounts {
			out.WriteString("\n• " + a.Provider)
			if a.Down {
				out.WriteString(locale.T(lang, "model.unavailable-mark"))
			}
			out.WriteString(":")
			wired := map[string]bool{}
			for _, m := range registry.Models {
				for i, hop := range m.Ladder {
					if hop.Provider != a.Provider {
						continue
					}
					wired[hop.Model] = true
					role := locale.T(lang, "model.role-backup")
					if i == 0 {
						role = locale.T(lang, "model.role-primary")
					}
					out.WriteString("\n    " + m.Name + " → " + hop.Model + " — " + role)
					if m.Name == current.Model || (hop.Model == current.Model && a.Provider == current.Provider && current.Provider != "") {
						out.WriteString(locale.T(lang, "model.current-mark"))
					}
				}
			}
			if registry.Switch == "catalog" {
				var extra []string
				for _, id := range a.Catalog {
					if wired[id] {
						continue
					}
					entry := a.Provider + "/" + id
					if id == current.Model && a.Provider == current.Provider {
						entry += locale.T(lang, "model.current-mark")
					}
					extra = append(extra, entry)
				}
				if len(extra) > 0 {
					out.WriteString("\n    " + locale.T(lang, "model.more") + strings.Join(extra, ", "))
				}
			}
		}
		out.WriteString("\n" + footer)
		return out.String()
	}
	for _, m := range registry.Models {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("• " + m.Name)
		if m.Name == current.Model {
			out.WriteString(locale.T(lang, "model.current-inline"))
		}
		out.WriteString(":")
		for i, hop := range m.Ladder {
			role := locale.T(lang, "model.role-backup")
			if i == 0 {
				role = locale.T(lang, "model.role-primary")
			}
			out.WriteString("\n    " + hop.Provider + " → " + hop.Model + " (" + role)
			if hop.Down {
				out.WriteString(locale.T(lang, "model.down-inline"))
			}
			out.WriteString(")")
		}
		out.WriteString("\n")
	}
	return out.String() + footer
}
