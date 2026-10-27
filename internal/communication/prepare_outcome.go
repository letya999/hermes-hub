package communication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

type prepareOutcomeRequest struct {
	OnboardingID     string `json:"onboarding_id"`
	Phase            string `json:"phase"`
	DefinitionID     string `json:"definition_id"`
	ContractID       string `json:"contract_id"`
	CredentialIntake string `json:"credential_intake"`
	BrokerRequestID  string `json:"broker_request_id"`
	Repository       string `json:"repository"`
	Detail           string `json:"detail"`
	FormURL          string `json:"form_url"`
	Event            string `json:"event"`
	Tools            int    `json:"tools"`
	// CreatedAt pins the onboarding creation instant so the gateway can find
	// the message job that was running when the install was requested.
	CreatedAt string `json:"created_at,omitempty"`
	// Browser viewer links carry no onboarding: the connection is the target.
	ConnectionID string `json:"connection_id,omitempty"`
	ViewerURL    string `json:"viewer_url,omitempty"`
}

// handlePrepareOutcome turns a finished ToolHub prepare into a channel reply
// and, when no user login is pending, a continuation job.
func (g *Gateway) handlePrepareOutcome(w http.ResponseWriter, r *http.Request) {
	caller, ok := g.authorizeControl(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request prepareOutcomeRequest
	if decodeJSON(r, &request) != nil {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.ConnectionID != "" || request.ViewerURL != "" {
		g.handleViewerNotice(w, caller, request)
		return
	}
	if !identity.ValidID(request.OnboardingID) || len(request.Repository) > 300 || len(request.Detail) > 400 {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.Event != "" {
		if !knownPrepareEvent(request.Event) || request.Phase == "" || len(request.Phase) > 40 {
			http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
			return
		}
	} else if !knownPreparePhase(request.Phase) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.Tools < 0 || request.Tools > 500 {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.DefinitionID != "" && !identity.ValidID(request.DefinitionID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.ContractID != "" && !identity.ValidID(request.ContractID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if len(request.CredentialIntake) > 40 {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if request.BrokerRequestID != "" && !identity.ValidID(request.BrokerRequestID) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	if !loopbackCredentialURL(request.FormURL) {
		http.Error(w, "invalid prepare outcome", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	if user.ID == "" {
		http.Error(w, "unknown principal", http.StatusForbidden)
		return
	}
	loginURL := g.telegramAuthInvite(caller, request)
	notice := prepareOutcomeNotice(locale.LangOf(user.WorkspaceDir), request, loginURL)
	key := prepareOutcomeKey(request, loginURL)
	if err := g.deliverPrepareNotice(user, caller, key, notice, request, loginURL != ""); err != nil {
		http.Error(w, "prepare outcome rejected", http.StatusBadRequest)
		return
	}
	// The channel-side link (QR invite, etc.) goes back to the caller so the
	// agent can print it in its own reply — the notice alone is easy to miss.
	if loginURL == "" {
		loginURL = request.FormURL
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"login_url": loginURL})
}

// handleViewerNotice relays the one-time browser viewer link minted by
// connector_status. The connection plays the routing role the onboarding id
// plays for prepare outcomes: origin fallback and message edit key use it.
func (g *Gateway) handleViewerNotice(w http.ResponseWriter, caller identity.Envelope, request prepareOutcomeRequest) {
	viewer := actionLink(request.ViewerURL, "/viewer/")
	if !identity.ValidID(request.ConnectionID) || !loopbackCredentialURL(request.ViewerURL) || viewer == "" {
		http.Error(w, "invalid viewer outcome", http.StatusBadRequest)
		return
	}
	user := g.user(caller.PrincipalID)
	if user.ID == "" {
		http.Error(w, "unknown principal", http.StatusForbidden)
		return
	}
	sum := sha256.Sum256([]byte(viewer))
	key := "viewer-" + request.ConnectionID + "-" + hex.EncodeToString(sum[:4])
	lang := locale.LangOf(user.WorkspaceDir)
	notice := locale.T(lang, "prepare.viewer", langLink(viewer, lang))
	route := prepareOutcomeRequest{OnboardingID: request.ConnectionID, Phase: "viewer", FormURL: viewer}
	if err := g.deliverPrepareNotice(user, caller, key, notice, route, true); err != nil {
		http.Error(w, "viewer notice rejected", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{})
}

func knownPreparePhase(phase string) bool {
	switch phase {
	case "awaiting-credentials", "awaiting-confirm", "awaiting-oauth", "confirmed", "enabled", "failed",
		// A remove/disable outcome posts the onboarding's current phase, not
		// an install phase — the /remove/ link carries the pending action.
		"disabled", "revoked", "cleanup_incomplete":
		return true
	default:
		return false
	}
}

// prepareProgressEvent marks an interim step, not a settled phase: the
// onboarding's phase alone would either fail validation or collapse distinct
// moments (a rejected credential submit returns to awaiting-credentials, the
// phase the form link already used).
func knownPrepareEvent(event string) bool {
	switch event {
	case "prepare-started", "credentials-check", "credentials-rejected", "binding-failed", "link-resent":
		return true
	default:
		return false
	}
}

func prepareOutcomeNotice(lang locale.Lang, request prepareOutcomeRequest, loginURL string) string {
	repo := strings.TrimSpace(request.Repository)
	if repo == "" {
		repo = locale.T(lang, "prepare.repo-fallback")
	}
	reason := func() string {
		if detail := strings.TrimSpace(request.Detail); detail != "" {
			return locale.T(lang, "prepare.reason", detail)
		}
		return ""
	}
	switch request.Event {
	case "prepare-started":
		return locale.T(lang, "prepare.started", repo)
	case "credentials-check":
		return locale.T(lang, "prepare.creds-check")
	case "credentials-rejected":
		return locale.T(lang, "prepare.creds-rejected", repo) + reason() + locale.T(lang, "prepare.creds-rejected-tail")
	case "binding-failed":
		return locale.T(lang, "prepare.binding-failed", repo) + reason() + locale.T(lang, "prepare.binding-failed-tail")
	}
	// A removal preview keeps the onboarding's prior phase, so the link path —
	// not the phase — names the pending human action.
	if remove := actionLink(request.FormURL, "/remove/"); remove != "" {
		return locale.T(lang, "prepare.remove-confirm", repo, langLink(remove, lang))
	}
	switch request.Phase {
	case "awaiting-credentials":
		if request.CredentialIntake == "telegram_qr" && loginURL != "" {
			text := locale.T(lang, "prepare.tg-qr", loginURL)
			if request.FormURL != "" {
				text += locale.T(lang, "prepare.form-fallback", langLink(request.FormURL, lang))
			}
			return text
		}
		text := locale.T(lang, "prepare.image-ready", repo)
		if formURL := strings.TrimSpace(request.FormURL); formURL != "" {
			text += locale.T(lang, "prepare.form-open", langLink(formURL, lang))
		} else {
			text += locale.T(lang, "prepare.form-next")
		}
		return text + locale.T(lang, "prepare.no-token")
	case "failed":
		return locale.T(lang, "prepare.failed", repo) + reason()
	case "awaiting-confirm":
		text := locale.T(lang, "prepare.ready-confirm", repo)
		if confirm := actionLink(request.FormURL, "/confirm/"); confirm != "" {
			return text + locale.T(lang, "prepare.confirm-link", langLink(confirm, lang))
		}
		// The stable wizard link already sits in the edited message — the
		// replacement text must keep it or the user loses the only URL.
		if wizardLink(request.FormURL) {
			return text + locale.T(lang, "prepare.confirm-link", langLink(strings.TrimSpace(request.FormURL), lang))
		}
		return text + locale.T(lang, "prepare.continuing")
	case "awaiting-oauth":
		text := locale.T(lang, "prepare.oauth-wait", repo)
		if connect := actionLink(request.FormURL, "/connect/"); connect != "" {
			return text + locale.T(lang, "prepare.oauth-open", langLink(connect, lang))
		}
		if device := actionLink(request.FormURL, "/device/"); device != "" {
			return text + locale.T(lang, "prepare.device-open", langLink(device, lang))
		}
		return text
	case "enabled":
		if request.Tools > 0 {
			return locale.T(lang, "prepare.enabled-tools", repo, request.Tools)
		}
		return locale.T(lang, "prepare.enabled", repo)
	default:
		return locale.T(lang, "prepare.unknown-phase", repo, request.Phase)
	}
}

// prepareContinuation composes the model-facing service message that resumes
// the agent after a settled prepare outcome. The instruction stays English —
// a neutral scaffold the model follows reliably — and the owner's language
// travels as an explicit hint so the visible reply lands in the user's own
// language instead of drifting to the instruction language.
func prepareContinuation(request prepareOutcomeRequest, lang locale.Lang) string {
	repo := strings.TrimSpace(request.Repository)
	if repo == "" {
		repo = "repository"
	}
	text := "Service continuation of an MCP install. prepare_source for " + repo + " finished, phase " + request.Phase + ", onboarding_id " + request.OnboardingID + ". Call mcp__toolhub__status with this onboarding_id. If the phase is awaiting-credentials, call mcp__toolhub__required_credentials and send the user form_url. Do not ask for a token in chat. Do not use the terminal and do not edit config.yaml."
	if request.Phase == "awaiting-oauth" && actionLink(request.FormURL, "/connect/") != "" {
		text = "Service continuation of an MCP install. The authorization link for " + repo + " was already sent to the user in the status message above — tell them to open it and sign in at the provider; do not repost or construct the link in chat. If the phase became enabled, announce the connection. If the user asks for the link or reports it does not work, call mcp__toolhub__status with onboarding_id " + request.OnboardingID + " and resend=true — the chat will get a fresh message with the link. Do not ask for a token in chat. Do not use the terminal and do not edit config.yaml."
	}
	if request.Phase == "awaiting-confirm" && actionLink(request.FormURL, "/confirm/") != "" {
		text = "Service continuation of an MCP install. The confirmation link for " + repo + " was already sent to the user in a separate message — tell them to open it. If they cannot see the link or it expired, call mcp__toolhub__status with onboarding_id " + request.OnboardingID + " and resend=true: the channel will send a new message. Do not construct the link and do not ask for data in chat."
	}
	if request.Phase == "awaiting-credentials" && strings.TrimSpace(request.FormURL) != "" {
		text = "Service continuation of an MCP install. The user was already sent the form link for " + repo + ", onboarding_id " + request.OnboardingID + ". Do not send another link and do not invent a network error. If the user says the form rejected the token, call mcp__toolhub__status and relay the error. Do not ask for a token in chat. Do not use the terminal and do not edit config.yaml."
	}
	if wizardLink(request.FormURL) && (request.Phase == "awaiting-credentials" || request.Phase == "awaiting-confirm" || request.Phase == "awaiting-oauth") {
		text = "Service continuation of an MCP install " + repo + ", phase " + request.Phase + ". The whole setup happens on one page: the link was already sent to the user — tell them to return to that same link; the page itself shows the current step (form, confirmation or Google sign-in). There are no other messages, buttons or confirmation links — do not mention any. If the link does not open or expired, call mcp__toolhub__status with onboarding_id " + request.OnboardingID + " and resend=true. Reply with one short message. Do not construct links and do not ask for data in chat."
	}
	return text + modelLangHint(lang)
}

// modelLangHint appends the owner's language to a model-facing instruction:
// English stays the instruction scaffold while the visible reply is pinned to
// the user's own language.
func modelLangHint(lang locale.Lang) string {
	return " The user's language is " + locale.Names[lang] + " (" + string(lang) + "); answer the user in that language."
}

// actionLink returns the validated human-action URL when its path carries the
// requested prefix; every other input — empty, unparseable, a different action —
// yields "" so callers can fall back to link-free text.
func actionLink(raw, prefix string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.HasPrefix(parsed.Path, prefix) {
		return ""
	}
	return strings.TrimSpace(raw)
}

// langLink stamps the recipient's language onto a toolhub link so the page
// opens in the same language as the chat. Toolhub pages are multi-principal
// and cannot see workspace prefs, so the language travels in the URL. The
// links here always carry an auth query (?k=/?t=), so & is the separator.
func langLink(link string, lang locale.Lang) string {
	return link + "&lang=" + string(lang)
}

// wizardLink reports whether the action URL is the stable single-link wizard
// page (/connect/<id>?k=<nonce>) rather than a one-time hop: those keep their
// own fresh-message rule because each is a different capability token.
func wizardLink(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.HasPrefix(parsed.Path, "/connect/") && parsed.Query().Get("k") != ""
}

// prepareOutcomeKey changes when a new form link is minted. Reusing the
// onboarding id with the previous link's payload made the notice return 400
// and the channel never received the fresh URL.
func prepareOutcomeKey(request prepareOutcomeRequest, loginURL string) string {
	key := "prepare-" + request.OnboardingID + "-" + request.Phase
	if request.Event != "" {
		key += "-" + request.Event
		// Two identical rejections carry the same key and dedupe away; a
		// different rejection reason must still reach the channel.
		if request.Event == "credentials-rejected" {
			sum := sha256.Sum256([]byte(request.Detail))
			key += "-" + hex.EncodeToString(sum[:4])
		}
	}
	if form := strings.TrimSpace(request.FormURL); form != "" {
		sum := sha256.Sum256([]byte(form))
		key += "-" + hex.EncodeToString(sum[:4])
	}
	if loginURL != "" {
		sum := sha256.Sum256([]byte(loginURL))
		key += "-" + hex.EncodeToString(sum[:4])
	}
	return key
}

func loopbackCredentialURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery == "" {
		return false
	}
	if parsed.Scheme == "https" {
		// Public links exist only when the operator configured one; the
		// notice host must be exactly that origin — no look-alike domains.
		origin, err := url.Parse(api.PublicOrigin())
		if err != nil || origin.Host == "" || parsed.Host != origin.Host {
			return false
		}
	} else if parsed.Scheme == "http" {
		switch parsed.Hostname() {
		case "127.0.0.1", "localhost", "::1":
		default:
			return false
		}
	} else {
		return false
	}
	return strings.HasPrefix(parsed.Path, "/credentials/") || strings.HasPrefix(parsed.Path, "/confirm/") || strings.HasPrefix(parsed.Path, "/remove/") || strings.HasPrefix(parsed.Path, "/connect/") || strings.HasPrefix(parsed.Path, "/device/") || strings.HasPrefix(parsed.Path, "/viewer/")
}

func (g *Gateway) deliverPrepareNotice(user User, caller identity.Envelope, key, notice string, request prepareOutcomeRequest, waitingForLogin bool) error {
	now := g.now().UTC()
	// The callback authenticates by principal only, so caller.ConversationID is
	// the synthesized DM envelope: route by the still-active message job that
	// initiated this onboarding, else the notice leaks to the DM root.
	conversation, deliveryTarget := caller.ConversationID, caller.DeliveryTargetID
	var bound Task
	if len(user.TelegramIDs) > 0 {
		bound = g.spool.TaskByConversation(user, user.TelegramIDs[0], conversation)
	}
	// The initiating tool call runs inside its message job, so the job that
	// existed — and most recently started — at the onboarding's creation
	// instant is the initiator. It outranks a persisted route, which older
	// builds may have bound through the unsafe newest-active heuristic.
	origin, found := prepareOrigin{}, false
	if created, err := time.Parse(time.RFC3339Nano, request.CreatedAt); err == nil && !created.IsZero() {
		if mapping, ok := g.spool.initiatingMessageMapping(user.ID, created, request.Repository); ok {
			origin = prepareOrigin{ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, Channel: mapping.Channel, ChatID: mapping.ChatID, TopicID: mapping.TopicID}
			found = true
		}
	}
	if !found {
		if persisted, ok := g.spool.loadPrepareOrigin(request.OnboardingID); ok {
			origin, found = persisted, true
		}
	}
	if !found {
		if mapping, ok := g.spool.newestActiveMessageMapping(user.ID); ok {
			origin = prepareOrigin{ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, Channel: mapping.Channel, ChatID: mapping.ChatID, TopicID: mapping.TopicID}
			found = true
		}
	}
	if found {
		// The origin job ends long before the build settles; persist the route
		// so later outcomes still reach the same session.
		_ = g.spool.putPrepareOrigin(request.OnboardingID, origin)
	}
	if found && origin.ConversationID != "" {
		bound = g.spool.TaskByConversation(user, origin.ChatID, origin.ConversationID)
		conversation, deliveryTarget = origin.ConversationID, origin.DeliveryTargetID
	}
	// The notice returns on the channel the human asked from. Channels without
	// Caps().Actions get the same text — the link is plain and clickable.
	channelName := origin.Channel
	if channelName == "" {
		channelName = bound.Channel
	}
	if channelName == "" {
		channelName = "telegram_bot"
	}
	chatID, topicID := bound.ChatID, bound.TopicID
	if channelName == "telegram_bot" {
		if len(user.TelegramIDs) == 0 {
			return errNoPrepareChannel
		}
		if chatID == 0 {
			chatID = user.TelegramIDs[0]
		}
	} else if chatID == 0 {
		chatID, topicID = origin.ChatID, origin.TopicID
	}
	delivery := Delivery{ID: key + "-notice", IdempotencyKey: key + "-notice", Channel: channelName, ChatID: chatID, TaskID: bound.TaskID, TopicID: topicID, Text: notice, CreatedAt: now}
	// Each new action link lands as a fresh message; the key dedupes repeats.
	// Editing the old status can bury the only link the user needs to
	// continue. The stable wizard link is exempt: it stays the same URL for
	// the whole flow, so a phase notice refreshes the message in place.
	if request.Event != "link-resent" && (request.FormURL == "" || wizardLink(request.FormURL)) {
		delivery.EditMessageID = g.spool.prepareMessageID(request.OnboardingID)
	}
	if err := g.spool.EnqueueDelivery(delivery); err != nil {
		return err
	}
	if request.Event != "" || waitingForLogin || actionLink(request.FormURL, "/remove/") != "" {
		// Wait for user action before resuming the agent — an armed remove
		// link is itself the continuation.
		return nil
	}
	envelope := caller
	envelope.ConversationID = conversation
	envelope.DeliveryTargetID = deliveryTarget
	if bound.ChatID < 0 && deliveryTarget == "" {
		envelope.DeliveryTargetID = "telegram-" + strconv.FormatInt(bound.ChatID, 10)
	}
	job := Job{
		Envelope: envelope, ID: key, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID,
		ScopeID: "user:" + user.ID, Channel: channelName, Trigger: "prepare", IdempotencyKey: key,
		ChatID: chatID, TaskID: bound.TaskID, TopicID: topicID,
		Text: prepareContinuation(request, locale.LangOf(user.WorkspaceDir)), CreatedAt: now,
	}
	// The outcome key tracks the action link, not this job's payload: a
	// resend under a stable wizard link can carry a drifted envelope or
	// reworded text, which must enqueue afresh instead of colliding with
	// the spent job's fingerprint.
	jobKey := key + "-" + jobFingerprint(job)[:8]
	job.ID, job.IdempotencyKey = jobKey, jobKey
	_, err := g.spool.Enqueue(job)
	return err
}

var errNoPrepareChannel = errString("user has no channel")

type errString string

func (e errString) Error() string { return string(e) }
