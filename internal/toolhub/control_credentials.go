package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
)

func (c *ControlPlane) acceptCredentialGate(ctx context.Context, auth identity.Envelope, preparing api.Onboarding, review api.SourceReview) (map[string]any, error) {
	definition := review.Definition
	brokerGate := definition.CredentialContractID != ""
	if brokerGate && (c.Broker == nil || !c.Broker.Enabled()) {
		return nil, fmt.Errorf("%w: reviewed credential Broker is required for authenticated preflight", api.ErrIsolation)
	}
	if len(requiredSecretNames(definition)) == 0 && !brokerGate {
		return nil, fmt.Errorf("%w: credential gate named no secrets", api.ErrInvalid)
	}
	names := requiredCredentialNames(definition)
	hints := credentialGateHints(names, review.AdmissionGroups)
	if len(hints) == 0 {
		return nil, fmt.Errorf("%w: credential gate named no secrets", api.ErrInvalid)
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
	onboarding.Phase = api.PhaseAwaitingCreds
	onboarding.Required = hints
	onboarding.AdmissionPending = true
	onboarding.Error = api.PublicPrepareError(errors.New(review.AdmissionDetail))
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
		onboarding.FormNonce = api.RandomNonce()
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

func requiredSecretNames(definition api.ToolDefinition) []string {
	names := make([]string, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		if input.Required && api.SecretName(input.Name) {
			names = append(names, input.Name)
		}
	}
	return names
}

func credentialGateHints(names []string, groups [][]string) []api.CredentialHint {
	groupOf := map[string]int{}
	for index, group := range groups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]api.CredentialHint, 0, len(names))
	for _, name := range names {
		hints = append(hints, api.CredentialHint{
			Name: name, Type: api.ConnectionType(name), Secret: api.SecretName(name),
			Delivery: "env", Target: name, Hint: "protected loopback form",
			AlternativeGroup: groupOf[name],
		})
	}
	return hints
}

// credentialValueLineSafe rejects control characters in values that land on
// an env line or HTTP header: a newline injects sibling variables. File
// delivery is exempt (content is written verbatim to a mounted file), and a
// json-delivery field keeps newlines only when the value parses as JSON —
// otherwise it would fall through to an env line.
func credentialValueLineSafe(hint api.CredentialHint, value string) error {
	if hint.MaxLength > 0 && len(value) > hint.MaxLength {
		return fmt.Errorf("%w: %s exceeds %d bytes", api.ErrInvalid, hint.Name, hint.MaxLength)
	}
	if hint.Format != "" {
		pattern, err := regexp.Compile(hint.Format)
		if err != nil || !pattern.MatchString(value) {
			return fmt.Errorf("%w: %s does not match its declared format", api.ErrInvalid, hint.Name)
		}
	}
	if hint.Delivery == "file" {
		return nil
	}
	if !strings.ContainsAny(value, "\x00\r\n") {
		return nil
	}
	if hint.Delivery == "json" && json.Valid([]byte(value)) {
		return nil
	}
	return fmt.Errorf("%w: %s must not contain control characters", api.ErrInvalid, hint.Name)
}

func filterSubmittedCredentials(hints []api.CredentialHint, values map[string]string) (map[string]string, error) {
	alternatives := map[int][]api.CredentialHint{}
	filtered := map[string]string{}
	for _, hint := range hints {
		if hint.AlternativeGroup > 0 {
			alternatives[hint.AlternativeGroup] = append(alternatives[hint.AlternativeGroup], hint)
			continue
		}
		value := strings.TrimSpace(values[hint.Name])
		if value == "" {
			// The owner may demote a required field on the form itself
			// (opt:<name>): the submit is accepted without a value and the
			// readiness probe decides whether the server tolerates the gap.
			if strings.TrimSpace(values["opt:"+hint.Name]) != "" {
				continue
			}
			return nil, fmt.Errorf("%w: missing %s", api.ErrInvalid, hint.Name)
		}
		if err := credentialValueLineSafe(hint, value); err != nil {
			return nil, err
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
			if err := credentialValueLineSafe(hint, value); err != nil {
				return nil, err
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
		return nil, fmt.Errorf("%w: incomplete credential alternative: %s", api.ErrInvalid, strings.Join(missingNames, ","))
	}
	if matched != 1 {
		return nil, fmt.Errorf("%w: choose one credential alternative", api.ErrInvalid)
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

func (c *ControlPlane) noteFormRejection(onboarding api.Onboarding, err error) {
	if c == nil || c.Store == nil || err == nil || !errors.Is(err, api.ErrInvalid) {
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
		return api.PublicPrepareError(err)
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
		labels = append(labels, credentialFieldLabel(api.CredentialHint{Name: name}))
	}
	return strings.Join(labels, ", ")
}

// credentialProbeFailure is the text shown on the form and stored for the
// channel. It keeps the server's message and drops stack traces.
func credentialProbeFailure(err error) string {
	text := api.PublicPrepareError(err)
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

// persistFailure records the public failure reason on the onboarding before
// returning the original error. A failed state write is folded into the
// returned error instead of silently dropping both cause and state update.
func (c *ControlPlane) persistFailure(onboarding api.Onboarding, errorText string, cause error) error {
	onboarding.Error = errorText
	onboarding.ErrorCode = api.ErrorCode(cause)
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return fmt.Errorf("%w (recording failure state failed: %v)", cause, err)
	}
	return cause
}

func (c *ControlPlane) requiredCredentials(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
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
	if err := c.refreshCredentialForm(ctx, auth, &onboarding); err != nil {
		return nil, err
	}
	body := c.statusBody(onboarding, true)
	if onboarding.Phase == api.PhaseAwaitingCreds && onboarding.FormNonce != "" && c.now().Before(onboarding.FormExpires) {
		body["input"] = "localhost-form"
	}
	if onboarding.BrokerRequestID != "" && onboarding.BrokerAuthorizationURL != "" && onboarding.Phase == api.PhaseAwaitingCreds {
		body["input"] = "credential-broker"
		body["broker_request_id"] = onboarding.BrokerRequestID
	}
	if c.OAuth != nil {
		body["oauth"] = "pkce"
	}
	return body, nil
}

// refreshCredentialForm makes an idempotent resume useful after the original
// loopback link expires. The old nonce is replaced, so an old URL cannot be
// replayed while the same request key still identifies the onboarding.
func (c *ControlPlane) refreshCredentialForm(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding) error {
	if onboarding == nil || onboarding.Phase != api.PhaseAwaitingCreds {
		return nil
	}
	if onboarding.BrokerRequestID != "" || onboarding.AdmissionPending && onboarding.BrokerCredentialID != "" {
		return nil
	}
	now := c.now()
	if onboarding.FormNonce != "" && now.Before(onboarding.FormExpires) {
		return nil
	}
	onboarding.FormNonce = api.RandomNonce()
	onboarding.FormExpires = now.Add(c.ttl())
	onboarding.Revision++
	if err := c.Store.PutOnboarding(*onboarding); err != nil {
		return err
	}
	api.PostPrepareNotice(ctx, auth, *onboarding, "", 0)
	return nil
}

func (c *ControlPlane) refreshConfirmation(ctx context.Context, auth identity.Envelope, onboarding *api.Onboarding) error {
	if onboarding == nil || onboarding.Phase != api.PhaseAwaitingConfirm || (onboarding.ConfirmationNonce != "" && c.now().Before(onboarding.ConfirmationExpires)) {
		return nil
	}
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	if err := c.Store.PutOnboarding(*onboarding); err != nil {
		return err
	}
	// A fresh nonce means a fresh link — the channel must carry it because the
	// tool result never will.
	api.PostPrepareNotice(ctx, auth, *onboarding, "", 0)
	return nil
}

func (c *ControlPlane) SubmitCredentials(onboardingID, nonce string, values map[string]string) error {
	if c == nil || c.Store == nil || c.Secrets == nil {
		return fmt.Errorf("%w: credential store", api.ErrInvalid)
	}
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	if onboarding.AdmissionPending && onboarding.Definition != nil && onboarding.Definition.CredentialContractID != "" {
		return fmt.Errorf("%w: contracted credentials must be submitted to Broker", api.ErrUnauthorized)
	}
	if onboarding.FormNonce == "" || nonce != onboarding.FormNonce {
		return fmt.Errorf("%w: form nonce", api.ErrUnauthorized)
	}
	if onboarding.FormExpires.IsZero() || c.now().After(onboarding.FormExpires) {
		return fmt.Errorf("%w: expired form", api.ErrUnauthorized)
	}
	if onboarding.Phase != api.PhaseAwaitingCreds && onboarding.Phase != api.PhaseAwaitingConfirm {
		return fmt.Errorf("%w: credentials not expected", api.ErrUnauthorized)
	}
	filtered, err := filterSubmittedCredentials(onboarding.Required, values)
	if err != nil {
		c.noteFormRejection(onboarding, err)
		return err
	}
	// Optional inputs filled on the same form are stored and delivered like any
	// other value; empty ones simply stay absent.
	for _, hint := range onboarding.Optional {
		if value := strings.TrimSpace(values[hint.Name]); value != "" {
			if err := credentialValueLineSafe(hint, value); err != nil {
				return err
			}
			filtered[hint.Name] = value
		}
	}
	authEnv := onboardingEnvelope(onboarding)
	probedByAdmission := onboarding.AdmissionPending
	if onboarding.AdmissionPending {
		definition, defErr := c.definitionOf(onboarding)
		if defErr != nil {
			return defErr
		}
		if c.AdmitWithCredentials == nil {
			return fmt.Errorf("%w: credential admission probe is not configured", api.ErrInvalid)
		}
		onboarding.Error = "Проверяю поля у сервера."
		onboarding.Revision++
		if err := c.Store.PutOnboarding(onboarding); err != nil {
			return err
		}
		// The admission probe below can hold the form POST for tens of
		// seconds; tell the channel a real check is running, not a hang.
		api.DeliverPrepareEvent(context.Background(), authEnv, onboarding, "credentials-check")
		admitted, admitErr := c.AdmitWithCredentials(context.Background(), definition, filtered)
		if admitErr != nil {
			failure := "Проверка не прошла. " + credentialProbeFailure(admitErr)
			if persistErr := c.persistFailure(onboarding, failure, admitErr); persistErr != admitErr {
				return persistErr
			}
			rejected := onboarding
			rejected.Error = failure
			api.DeliverPrepareEvent(context.Background(), authEnv, rejected, "credentials-rejected")
			return fmt.Errorf("%w: %s", api.ErrIsolation, failure)
		}
		if regErr := c.registerAdmittedDefinition(&admitted, onboarding.PrincipalID); regErr != nil {
			return c.persistFailure(onboarding, credentialProbeFailure(regErr), regErr)
		}
		if admitted.Workload.Class == api.Shared {
			if err := c.Store.PromoteToCatalog(admitted.DefinitionID, admitted.Version, onboarding.PrincipalID); err != nil {
				return c.persistFailure(onboarding, credentialProbeFailure(err), err)
			}
		}
		onboarding.Definition = &admitted
		onboarding.DefinitionID = admitted.DefinitionID
		onboarding.DefinitionVersion = admitted.Version
		onboarding.Permissions = api.ToolNames(admitted)
		onboarding.Effects = api.EffectNames(admitted)
		onboarding.ReviewDigest = admitted.Source.ReviewDigest
		onboarding.AdmissionPending = false
		onboarding.Error = ""
		onboarding.ErrorCode = ""
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
	// S2.6: nothing persists until a read-only probe proves the credentials.
	// The admission path probed above; an already-admitted credentialed remote
	// definition gets the same tools/list check before the ciphertext write.
	// Shared-policy submissions skip it — the workload uses the policy-owned
	// locator, so submitted values never reach the server anyway.
	if !shared && !probedByAdmission && c.AdmitWithCredentials != nil {
		definition, defErr := c.definitionOf(onboarding)
		if defErr == nil && definition.Transport == api.RemoteMCP && len(definition.Credentials) > 0 {
			if _, probeErr := c.AdmitWithCredentials(context.Background(), definition, filtered); probeErr != nil {
				failure := "Проверка не прошла. " + credentialProbeFailure(probeErr)
				if persistErr := c.persistFailure(onboarding, failure, probeErr); persistErr != probeErr {
					return persistErr
				}
				return fmt.Errorf("%w: %s", errCredentialProbe, failure)
			}
		}
	}
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
		return fmt.Errorf("%w: shared locator", api.ErrInvalid)
	}
	onboarding.Locator = locator
	onboarding.CredentialOwner = owner
	onboarding.FormNonce = ""
	onboarding.FormExpires = time.Time{}
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = api.RandomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

// errCredentialSaved marks a failure after the loopback secret is already stored.
var errCredentialSaved = errors.New("credential saved")

// errCredentialProbe marks a pre-persistence probe rejection: nothing was
// stored, so the form re-renders instead of reporting a saved secret.
var errCredentialProbe = errors.New("credential probe rejected")

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
			api.DeliverPrepareEvent(ctx, onboardingEnvelope(latest), notify, "binding-failed")
		}
		return fmt.Errorf("%w: %w", errCredentialSaved, err)
	}
	return nil
}

func credentialHints(definition api.ToolDefinition) []api.CredentialHint {
	groupOf := map[string]int{}
	for index, group := range definition.CredentialGroups {
		for _, name := range group {
			groupOf[name] = index + 1
		}
	}
	hints := make([]api.CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		upper := strings.ToUpper(input.Name)
		// Every required stored input gates onboarding, not only secret-named
		// ones — a URL-only contract otherwise skips the credentials phase and
		// deadlocks at confirm on a locator that was never collected. Per-request
		// inputs are supplied at call time, not stored during onboarding.
		if !input.Required || input.PerRequest {
			continue
		}
		kind := api.ConnectionType(input.Name)
		delivery := "env"
		if strings.Contains(upper, "CREDENTIALS") {
			kind, delivery = "json", "json"
		}
		// A reviewed delivery/target on the input wins over the name
		// heuristics: the form renders and submits what the contract says.
		if input.Delivery != "" {
			delivery = input.Delivery
		}
		target := input.Name
		if input.Target != "" {
			target = input.Target
		}
		hints = append(hints, api.CredentialHint{Name: input.Name, Type: kind, Secret: api.SecretName(input.Name), Delivery: delivery, Target: target, AlternativeGroup: groupOf[input.Name], Hint: "protected loopback form", Source: input.Source, Format: input.Format, MaxLength: input.MaxLength})
	}
	return hints
}

func credentialHintsForRecipe(definition api.ToolDefinition, recipe api.ConnectionRecipe) []api.CredentialHint {
	hints := make([]api.CredentialHint, 0, len(recipe.Fields))
	for _, field := range recipe.Fields {
		if !field.Required {
			continue
		}
		typ := field.Type
		if typ == "" {
			typ = "secret"
		}
		hint := "protected loopback form"
		hints = append(hints, api.CredentialHint{
			Name: field.Name, Type: typ, Secret: field.Secret, Delivery: field.Delivery,
			Target: field.Target, Alternative: field.Alternative, Hint: hint, Source: field.Source,
			Format: field.Format, MaxLength: field.MaxLength,
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

// optionalCredentialHints mirrors credentialHints for inputs the definition
// declares as non-required. The user still sees them on the protected form —
// upstream sources routinely declare useful knobs (default project, API URL)
// that must be editable without gating onboarding.
func optionalCredentialHints(definition api.ToolDefinition) []api.CredentialHint {
	hints := make([]api.CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		if input.Required || input.PerRequest || !contractCoverable(input) {
			continue
		}
		kind := api.ConnectionType(input.Name)
		delivery := "env"
		if strings.Contains(strings.ToUpper(input.Name), "CREDENTIALS") {
			kind, delivery = "json", "json"
		}
		if input.Delivery != "" {
			delivery = input.Delivery
		}
		target := input.Name
		if input.Target != "" {
			target = input.Target
		}
		hints = append(hints, api.CredentialHint{Name: input.Name, Type: kind, Secret: api.SecretName(input.Name), Delivery: delivery, Target: target, Hint: "protected loopback form", Source: input.Source, Format: input.Format, MaxLength: input.MaxLength})
	}
	return hints
}

func credentialNames(onboarding api.Onboarding) []string {
	names := make([]string, 0, len(onboarding.Required))
	for _, hint := range onboarding.Required {
		names = append(names, hint.Name)
	}
	return names
}

// brokerCredentialKeys lists the names a broker-bound credential reference
// covers. The contract env map already carries every collectable input —
// required or optional — while onboarding.Required only names the required
// ones, so an all-optional definition would otherwise produce an empty key
// set that reference validation rejects.
func brokerCredentialKeys(definition api.ToolDefinition, onboarding api.Onboarding) []string {
	if len(definition.CredentialContractEnv) == 0 {
		return credentialNames(onboarding)
	}
	seen := map[string]bool{}
	names := make([]string, 0, len(definition.CredentialContractEnv)+len(onboarding.Required))
	for name := range definition.CredentialContractEnv {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	for _, hint := range onboarding.Required {
		if !seen[hint.Name] {
			seen[hint.Name] = true
			names = append(names, hint.Name)
		}
	}
	slices.Sort(names)
	return names
}
