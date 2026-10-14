package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func (c *ControlPlane) acceptCredentialGate(ctx context.Context, auth identity.Envelope, preparing Onboarding, review SourceReview) (map[string]any, error) {
	definition := review.Definition
	brokerGate := definition.CredentialContractID != ""
	if brokerGate && (c.Broker == nil || !c.Broker.Enabled()) {
		return nil, fmt.Errorf("%w: reviewed credential Broker is required for authenticated preflight", ErrIsolation)
	}
	if len(requiredSecretNames(definition)) == 0 && !brokerGate {
		return nil, fmt.Errorf("%w: credential gate named no secrets", ErrInvalid)
	}
	names := requiredCredentialNames(definition)
	hints := credentialGateHints(names, review.AdmissionGroups)
	if len(hints) == 0 {
		return nil, fmt.Errorf("%w: credential gate named no secrets", ErrInvalid)
	}
	if len(definition.CredentialGroups) == 0 {
		known := map[string]bool{}
		for _, name := range names {
			known[name] = true
		}
		for _, group := range review.AdmissionGroups {
			var members []string
			for _, name := range group {
				if known[name] {
					members = append(members, name)
				}
			}
			if len(members) > 0 {
				definition.CredentialGroups = append(definition.CredentialGroups, members)
			}
		}
		if len(definition.CredentialGroups) < 2 {
			definition.CredentialGroups = nil
		}
	}
	onboarding := preparing
	if latest, err := c.Store.onboarding(preparing.OnboardingID); err == nil {
		onboarding = latest
	}
	// The draft is not a tool contract. Placeholder import tools must not be
	// listed or registered until tools/list succeeds with the submitted secrets.
	definition.Tools = nil
	definition.Source.ToolContractDigest = ""
	definition.Source.ToolContractSource = ""
	onboarding.Phase = PhaseAwaitingCreds
	onboarding.Required = hints
	onboarding.AdmissionPending = true
	onboarding.Error = publicPrepareError(errors.New(review.AdmissionDetail))
	onboarding.Permissions = nil
	onboarding.Effects = nil
	onboarding.Definition = &definition
	if definition.DefinitionID != "" {
		onboarding.DefinitionID = definition.DefinitionID
	}
	if definition.Version != "" {
		onboarding.DefinitionVersion = definition.Version
	}
	onboarding.ReviewDigest = ""
	if brokerGate {
		onboarding.FormNonce = ""
		onboarding.FormExpires = time.Time{}
	} else {
		onboarding.FormNonce = randomNonce()
		onboarding.FormExpires = c.now().Add(c.ttl())
	}
	onboarding.ConfirmationNonce = ""
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	if brokerGate {
		if err := c.ensureBrokerRequest(ctx, auth, &onboarding, definition); err != nil {
			return nil, err
		}
	}
	return c.statusBody(onboarding, false), nil
}

func requiredSecretNames(definition ToolDefinition) []string {
	names := make([]string, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		if input.Required && secretName(input.Name) {
			names = append(names, input.Name)
		}
	}
	return names
}

func credentialGateHints(names []string, groups [][]string) []CredentialHint {
	groupOf := map[string]int{}
	for index, group := range groups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]CredentialHint, 0, len(names))
	for _, name := range names {
		hints = append(hints, CredentialHint{
			Name: name, Type: connectionType(name), Secret: secretName(name),
			Delivery: "env", Target: name, Hint: "protected loopback form",
			AlternativeGroup: groupOf[name],
		})
	}
	return hints
}

func filterSubmittedCredentials(hints []CredentialHint, values map[string]string) (map[string]string, error) {
	alternatives := map[int][]CredentialHint{}
	filtered := map[string]string{}
	for _, hint := range hints {
		if hint.AlternativeGroup > 0 {
			alternatives[hint.AlternativeGroup] = append(alternatives[hint.AlternativeGroup], hint)
			continue
		}
		value := strings.TrimSpace(values[hint.Name])
		if value == "" {
			return nil, fmt.Errorf("%w: missing %s", ErrInvalid, hint.Name)
		}
		filtered[hint.Name] = value
	}
	if len(alternatives) == 0 {
		return filtered, nil
	}
	matched := 0
	partial := false
	var missingNames []string
	var chosen map[string]string
	completeGroups := []map[string]string{}
	for _, group := range alternatives {
		filled := map[string]string{}
		missing := false
		for _, hint := range group {
			value := strings.TrimSpace(values[hint.Name])
			if value == "" {
				missing = true
				continue
			}
			filled[hint.Name] = value
		}
		if missing {
			if len(filled) > 0 {
				partial = true
				for _, hint := range group {
					if strings.TrimSpace(values[hint.Name]) == "" {
						missingNames = append(missingNames, hint.Name)
					}
				}
			}
			continue
		}
		if len(filled) > 0 {
			matched++
			chosen = filled
			completeGroups = append(completeGroups, filled)
		}
	}
	if matched != 1 {
		if picked, ok := pickDuplicatedAlternative(completeGroups); ok {
			chosen = picked
			matched = 1
		}
	}
	if matched == 0 && partial {
		return nil, fmt.Errorf("%w: incomplete credential alternative: %s", ErrInvalid, strings.Join(missingNames, ","))
	}
	if matched != 1 {
		return nil, fmt.Errorf("%w: choose one credential alternative", ErrInvalid)
	}
	for name, value := range chosen {
		filtered[name] = value
	}
	return filtered, nil
}

// pickDuplicatedAlternative keeps one group when the same secret was copied
// into every alternative. The kept group is the one whose env name contains
// that secret's prefix, the text before the first "-" or "_".
func pickDuplicatedAlternative(groups []map[string]string) (map[string]string, bool) {
	if len(groups) < 2 {
		return nil, false
	}
	uniq := map[string]struct{}{}
	for _, group := range groups {
		for _, value := range group {
			uniq[value] = struct{}{}
		}
	}
	if len(uniq) != 1 {
		return nil, false
	}
	var only string
	for value := range uniq {
		only = value
	}
	prefix := only
	if i := strings.IndexAny(prefix, "-_"); i > 0 {
		prefix = prefix[:i]
	}
	if len(prefix) < 3 || len(prefix) > 12 {
		return nil, false
	}
	var hits []map[string]string
	needle := strings.ToUpper(prefix)
	for _, group := range groups {
		for name := range group {
			if strings.Contains(strings.ToUpper(name), needle) {
				hits = append(hits, group)
				break
			}
		}
	}
	if len(hits) != 1 {
		return nil, false
	}
	return hits[0], true
}

func (c *ControlPlane) noteFormRejection(onboarding Onboarding, err error) {
	if c == nil || c.Store == nil || err == nil || !errors.Is(err, ErrInvalid) {
		return
	}
	onboarding.Error = credentialFormRejection(err)
	onboarding.Revision++
	if persistErr := c.Store.PutOnboarding(onboarding); persistErr != nil {
		log.Printf("toolhub: form rejection on %s not recorded: %v", onboarding.OnboardingID, persistErr)
	}
}

func credentialFormRejection(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "incomplete credential alternative"):
		if missing := missingAlternativeNames(text); missing != "" {
			return "В выбранном наборе заполнены не все поля. Не заполнено: " + missing + "."
		}
		return "В выбранном наборе заполнены не все поля."
	case strings.Contains(text, "choose one credential alternative"):
		return "Нужен один полный набор полей."
	case strings.Contains(text, "unrecognized credential alternative"):
		return "Это поле не входит в рецепт сервера."
	case strings.Contains(text, "missing "):
		return "Не заполнено обязательное поле."
	default:
		return publicPrepareError(err)
	}
}

func missingAlternativeNames(text string) string {
	const marker = "incomplete credential alternative:"
	index := strings.Index(text, marker)
	if index < 0 {
		return ""
	}
	var labels []string
	for _, part := range strings.Split(text[index+len(marker):], ",") {
		name := strings.TrimSpace(part)
		if name == "" || strings.ContainsAny(name, " \t") {
			break
		}
		labels = append(labels, credentialFieldLabel(CredentialHint{Name: name}))
	}
	return strings.Join(labels, ", ")
}

// credentialProbeFailure is the text shown on the form and stored for the
// channel. It keeps the server's message and drops stack traces.
func credentialProbeFailure(err error) string {
	text := publicPrepareError(err)
	if text == "" {
		return ""
	}
	var messages []string
	rest := text
	const marker = `"message":"`
	for {
		index := strings.Index(rest, marker)
		if index < 0 {
			break
		}
		rest = rest[index+len(marker):]
		end := strings.Index(rest, `"`)
		if end <= 0 || end > 180 {
			break
		}
		messages = append(messages, rest[:end])
		rest = rest[end+1:]
	}
	for i := len(messages) - 1; i >= 0; i-- {
		lower := strings.ToLower(messages[i])
		if strings.Contains(lower, "fail") || strings.Contains(lower, "invalid") || strings.Contains(lower, "error") || strings.Contains(lower, "denied") {
			return messages[i]
		}
	}
	if strings.Contains(text, "tools/list empty") {
		return "Сервер не ответил списком инструментов."
	}
	if len(messages) > 0 {
		return messages[len(messages)-1]
	}
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}

var secretValuePattern = regexp.MustCompile(`(?i)\b(xox[baprs]-[A-Za-z0-9-]{8,}|sk-[A-Za-z0-9]{16,}|[A-Fa-f0-9]{32,})\b`)

// persistFailure records the public failure reason on the onboarding before
// returning the original error. A failed state write is folded into the
// returned error instead of silently dropping both cause and state update.
func (c *ControlPlane) persistFailure(onboarding Onboarding, errorText string, cause error) error {
	onboarding.Error = errorText
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return fmt.Errorf("%w (recording failure state failed: %v)", cause, err)
	}
	return cause
}

func (c *ControlPlane) requiredCredentials(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.regenerateBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshBrokerRequest(context.Background(), auth, &onboarding); err != nil {
		return nil, err
	}
	if err := c.refreshCredentialForm(&onboarding); err != nil {
		return nil, err
	}
	body := c.statusBody(onboarding, true)
	if onboarding.Phase == PhaseAwaitingCreds && onboarding.FormNonce != "" && c.now().Before(onboarding.FormExpires) {
		body["input"] = "localhost-form"
		body["form_url"] = c.origin() + "/credentials/" + onboarding.OnboardingID + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
	}
	if onboarding.BrokerRequestID != "" && onboarding.BrokerAuthorizationURL != "" && onboarding.Phase == PhaseAwaitingCreds {
		body["input"] = "credential-broker"
		body["broker_request_id"] = onboarding.BrokerRequestID
		body["authorization_url"] = onboarding.BrokerAuthorizationURL
		delete(body, "form_url")
		delete(body, "form_path")
	}
	if c.OAuth != nil {
		body["oauth"] = "pkce"
	}
	return body, nil
}

// refreshCredentialForm makes an idempotent resume useful after the original
// loopback link expires. The old nonce is replaced, so an old URL cannot be
// replayed while the same request key still identifies the onboarding.
func (c *ControlPlane) refreshCredentialForm(onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingCreds {
		return nil
	}
	if onboarding.BrokerRequestID != "" || onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
		return nil
	}
	now := c.now()
	if onboarding.FormNonce != "" && now.Before(onboarding.FormExpires) {
		return nil
	}
	onboarding.FormNonce = randomNonce()
	onboarding.FormExpires = now.Add(c.ttl())
	onboarding.Revision++
	return c.Store.PutOnboarding(*onboarding)
}

func (c *ControlPlane) refreshConfirmation(onboarding *Onboarding) error {
	if onboarding == nil || onboarding.Phase != PhaseAwaitingConfirm || (onboarding.ConfirmationNonce != "" && c.now().Before(onboarding.ConfirmationExpires)) {
		return nil
	}
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(*onboarding)
}

func (c *ControlPlane) SubmitCredentials(onboardingID, nonce string, values map[string]string) error {
	if c == nil || c.Store == nil || c.Secrets == nil {
		return fmt.Errorf("%w: credential store", ErrInvalid)
	}
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	if onboarding.AdmissionPending && onboarding.Definition != nil && onboarding.Definition.CredentialContractID != "" {
		return fmt.Errorf("%w: contracted credentials must be submitted to Broker", ErrUnauthorized)
	}
	if onboarding.FormNonce == "" || nonce != onboarding.FormNonce {
		return fmt.Errorf("%w: form nonce", ErrUnauthorized)
	}
	if onboarding.FormExpires.IsZero() || c.now().After(onboarding.FormExpires) {
		return fmt.Errorf("%w: expired form", ErrUnauthorized)
	}
	if onboarding.Phase != PhaseAwaitingCreds && onboarding.Phase != PhaseAwaitingConfirm {
		return fmt.Errorf("%w: credentials not expected", ErrUnauthorized)
	}
	filtered, err := filterSubmittedCredentials(onboarding.Required, values)
	if err != nil {
		c.noteFormRejection(onboarding, err)
		return err
	}
	authEnv := onboardingEnvelope(onboarding)
	if onboarding.AdmissionPending {
		definition, defErr := c.definitionOf(onboarding)
		if defErr != nil {
			return defErr
		}
		if c.AdmitWithCredentials == nil {
			return fmt.Errorf("%w: credential admission probe is not configured", ErrInvalid)
		}
		onboarding.Error = "Проверяю поля у сервера."
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return err
		}
		// The admission probe below can hold the form POST for tens of
		// seconds; tell the channel a real check is running, not a hang.
		deliverPrepareEvent(context.Background(), authEnv, onboarding, "credentials-check")
		admitted, admitErr := c.AdmitWithCredentials(context.Background(), definition, filtered)
		if admitErr != nil {
			failure := "Проверка не прошла. " + credentialProbeFailure(admitErr)
			if persistErr := c.persistFailure(onboarding, failure, admitErr); persistErr != admitErr {
				return persistErr
			}
			rejected := onboarding
			rejected.Error = failure
			deliverPrepareEvent(context.Background(), authEnv, rejected, "credentials-rejected")
			return fmt.Errorf("%w: %s", ErrIsolation, failure)
		}
		if regErr := c.registerAdmittedDefinition(&admitted, onboarding.PrincipalID); regErr != nil {
			return c.persistFailure(onboarding, credentialProbeFailure(regErr), regErr)
		}
		onboarding.Definition = &admitted
		onboarding.DefinitionID = admitted.DefinitionID
		onboarding.DefinitionVersion = admitted.Version
		onboarding.Permissions = toolNames(admitted)
		onboarding.Effects = effectNames(admitted)
		onboarding.ReviewDigest = admitted.Source.ReviewDigest
		onboarding.AdmissionPending = false
		onboarding.Error = ""
	}
	locator := onboarding.Locator
	owner := onboarding.PrincipalID
	shared := false
	c.Store.mu.RLock()
	if policy := c.Store.sharedPolicyLocked(authEnv, onboarding.DefinitionID); policy != nil {
		locator = policy.Locator
		owner = policy.StoreOwner
		onboarding.CredentialOwner = policy.StoreOwner
		shared = true
	}
	c.Store.mu.RUnlock()
	if !shared {
		if locator == "" {
			locator, err = c.Secrets.NewLocator()
			if err != nil {
				return err
			}
		}
		if err := c.Secrets.Put(locator, owner, filtered); err != nil {
			return err
		}
	} else if locator == "" {
		return fmt.Errorf("%w: shared locator", ErrInvalid)
	}
	onboarding.Locator = locator
	onboarding.CredentialOwner = owner
	onboarding.FormNonce = ""
	onboarding.FormExpires = time.Time{}
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

// errCredentialSaved marks a failure after the loopback secret is already stored.
var errCredentialSaved = errors.New("credential saved")

// AuthorizeCredentials treats the protected form submission as the user's
// confirmation and completes the binding without another model round-trip.
func (c *ControlPlane) AuthorizeCredentials(ctx context.Context, onboardingID, nonce string, values map[string]string) error {
	if err := c.SubmitCredentials(onboardingID, nonce, values); err != nil {
		return err
	}
	if err := c.finishAuthorization(ctx, onboardingID); err != nil {
		if latest, lerr := c.Store.onboarding(onboardingID); lerr == nil {
			notify := latest
			notify.Error = err.Error()
			deliverPrepareEvent(ctx, onboardingEnvelope(latest), notify, "binding-failed")
		}
		return fmt.Errorf("%w: %w", errCredentialSaved, err)
	}
	return nil
}

func credentialHints(definition ToolDefinition) []CredentialHint {
	groupOf := map[string]int{}
	for index, group := range definition.CredentialGroups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		upper := strings.ToUpper(input.Name)
		// Every required stored input gates onboarding, not only secret-named
		// ones — a URL-only contract otherwise skips the credentials phase and
		// deadlocks at confirm on a locator that was never collected. Per-request
		// inputs are supplied at call time, not stored during onboarding.
		if !input.Required || input.PerRequest {
			continue
		}
		kind := connectionType(input.Name)
		delivery := "env"
		if strings.Contains(upper, "CREDENTIALS") {
			kind, delivery = "json", "json"
		}
		hints = append(hints, CredentialHint{Name: input.Name, Type: kind, Secret: secretName(input.Name), Delivery: delivery, Target: input.Name, AlternativeGroup: groupOf[input.Name], Hint: "protected loopback form"})
	}
	return hints
}

func credentialHintsForRecipe(definition ToolDefinition, recipe ConnectionRecipe) []CredentialHint {
	hints := make([]CredentialHint, 0, len(recipe.Fields))
	for _, field := range recipe.Fields {
		if !field.Required {
			continue
		}
		typ := field.Type
		if typ == "" {
			typ = "secret"
		}
		hint := "protected loopback form"
		hints = append(hints, CredentialHint{
			Name: field.Name, Type: typ, Secret: field.Secret, Delivery: field.Delivery,
			Target: field.Target, Alternative: field.Alternative, Hint: hint,
		})
	}
	if len(hints) == 0 {
		return credentialHints(definition)
	}
	for index := range hints {
		for _, alternative := range recipe.Alternatives {
			if alternative.URL != "" {
				hints[index].AlternativeURL = alternative.URL
				break
			}
		}
	}
	return hints
}

func credentialNames(onboarding Onboarding) []string {
	names := make([]string, 0, len(onboarding.Required))
	for _, hint := range onboarding.Required {
		names = append(names, hint.Name)
	}
	return names
}
