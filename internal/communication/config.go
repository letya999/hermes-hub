package communication

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"gopkg.in/yaml.v3"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var telegramIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var spoolIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var gatewayOwnedEnv = map[string]bool{
	"TELEGRAM_BOT_TOKEN": true, "TELEGRAM_ALLOWED_USERS": true,
	"SLACK_SIGNING_SECRET": true, "SLACK_BOT_TOKEN": true, "SLACK_APP_TOKEN": true, "SLACK_ALLOWED_USERS": true,
}

// SlackLink is a verified workspace+sender pair. Email and display name never fill it.
type SlackLink struct {
	TeamID string `yaml:"team_id" json:"team_id"`
	UserID string `yaml:"user_id" json:"user_id"`
}

func (l SlackLink) key() string {
	return strings.ToLower(strings.TrimSpace(l.TeamID)) + "/" + strings.ToLower(strings.TrimSpace(l.UserID))
}

// User is the stable internal user plus its channel bindings and runtime roots.
type User struct {
	RuntimeID      string            `yaml:"runtime_id,omitempty" json:"runtime_id,omitempty"`
	PolicyVersion  string            `yaml:"policy_version,omitempty" json:"policy_version,omitempty"`
	ID             string            `yaml:"id" json:"id"`
	Enabled        bool              `yaml:"enabled" json:"enabled"`
	TelegramIDs    []int64           `yaml:"telegram_ids" json:"telegram_ids"`
	SlackIDs       []SlackLink       `yaml:"slack_ids,omitempty" json:"slack_ids,omitempty"`
	StateDir       string            `yaml:"state_dir" json:"state_dir"`
	WorkspaceDir   string            `yaml:"workspace_dir" json:"workspace_dir"`
	Features       []string          `yaml:"features,omitempty" json:"features,omitempty"`
	PolicyDisabled []string          `yaml:"policy_disabled,omitempty" json:"policy_disabled,omitempty"`
	ConfiguredEnv  map[string]bool   `yaml:"-" json:"-"`
	Env            map[string]string `yaml:"-" json:"-"`
}

// envelope binds each verified channel user independently of process-wide env.
func (user User) envelope(externalID int64) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.TelegramEnvelope(user.ID, externalID, runtimeID, policy)
}

func (user User) slackEnvelope(teamID, slackUser string) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.SlackEnvelope(user.ID, teamID, slackUser, runtimeID, policy)
}

// Config is the channel-neutral gateway configuration. Telegram is v1's adapter.
type Config struct {
	Supervised         bool   `yaml:"-"`
	OrganizationID     string `yaml:"organization_id"`
	Users              []User `yaml:"users"`
	TelegramToken      string `yaml:"-"`
	APIBaseURL         string `yaml:"api_base_url,omitempty"`
	SlackSigningSecret string `yaml:"-"`
	SlackBotToken      string `yaml:"-"`
	ControlAuth        string `yaml:"-"`
	// ControlTokensFile optionally points at the infra owner's
	// toolhub-tokens.json: sibling-space runtime tokens enrolled there are
	// equally valid control-plane bearers. The principal still comes from
	// X-Hub-Principal — the token only proves the caller is a hub service.
	ControlTokensFile string        `yaml:"-"`
	controlAuthExtra  []string      `yaml:"-"`
	ListenAddr        string        `yaml:"-"`
	FormOrigin        string        `yaml:"-"`
	NativeCron        string        `yaml:"-"`
	STTCommand        string        `yaml:"-"`
	TTSCommand        string        `yaml:"-"`
	STTURL            string        `yaml:"-"`
	STTAuth           string        `yaml:"-"`
	TTSURL            string        `yaml:"-"`
	TTSAuth           string        `yaml:"-"`
	TTSVoice          string        `yaml:"-"`
	TTSUploadURL      string        `yaml:"-"`
	STTTimeout        time.Duration `yaml:"-"`
	MediaMaxDuration  int           `yaml:"-"`
	SpoolDir          string        `yaml:"spool_dir"`
	RuntimeURL        string        `yaml:"-"`
	RuntimeAuth       string        `yaml:"-"`
	PollTimeout       time.Duration `yaml:"-"`
	HermesCommand     string        `yaml:"-"`
	CredentialStore   string        `yaml:"-"`
	CredentialKeyFile string        `yaml:"-"`
	ToolHubStore      string        `yaml:"-"`
	// ToolHubURL is the ToolHub control address (e.g. http://toolhub:8090)
	// used by /connections when the local registry is unavailable in
	// broker-approve mode. The caller's token comes from ControlTokensFile.
	ToolHubURL          string                  `yaml:"-"`
	TelegramAuthEnabled bool                    `yaml:"-"`
	TelegramAuthAPIID   string                  `yaml:"-"`
	TelegramAuthAPIHash string                  `yaml:"-"`
	AuditLedger         string                  `yaml:"-"`
	BrokerApprove       credentialbroker.Config `yaml:"-"`
	// Workers bounds concurrent job execution; contexts serialize per
	// (principal_id, context_id), different contexts run in parallel (ADR-0025).
	Workers int `yaml:"-"`
	// SpoolRetention bounds how long terminal spool history is kept; zero
	// means the 336-hour default (SPEC-0053 R2.3).
	SpoolRetention time.Duration `yaml:"-"`
	// MockDir relocates mock-channel capture outside the production spool;
	// empty keeps the historical spool/mock-* location (SPEC-0053 R2.3).
	MockDir string `yaml:"-"`
}

func (c Config) Validate() error {
	if c.Supervised && c.RuntimeURL == "" {
		return errors.New("supervised gateway requires the host runtime URL")
	}
	if !idPattern.MatchString(c.OrganizationID) {
		return errors.New("organization_id must be a lowercase identifier")
	}
	if c.SpoolDir == "" {
		return errors.New("spool_dir is required")
	}
	if c.RuntimeURL != "" {
		u, err := url.Parse(c.RuntimeURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("runtime_url must be an HTTP(S) URL without credentials, query or fragment")
		}
		if c.RuntimeAuth == "" {
			return errors.New("runtime auth is required")
		}
	}
	seenUsers, seenTelegram, seenSlack := map[string]bool{}, map[int64]bool{}, map[string]bool{}
	rootOwners := map[string]string{}
	for _, user := range c.Users {
		if !idPattern.MatchString(user.ID) || seenUsers[user.ID] {
			return fmt.Errorf("invalid or duplicate user %q", user.ID)
		}
		seenUsers[user.ID] = true
		if user.RuntimeID != "" && !idPattern.MatchString(user.RuntimeID) {
			return fmt.Errorf("user %q has an invalid runtime_id", user.ID)
		}
		if user.PolicyVersion != "" && !idPattern.MatchString(user.PolicyVersion) {
			return fmt.Errorf("user %q has an invalid policy_version", user.ID)
		}
		if !user.Enabled || user.StateDir == "" || user.WorkspaceDir == "" {
			continue
		}
		if filepath.Clean(user.StateDir) == filepath.Clean(user.WorkspaceDir) {
			return fmt.Errorf("user %q state and workspace must be separate", user.ID)
		}
		for label, root := range map[string]string{"state": user.StateDir, "workspace": user.WorkspaceDir} {
			key, err := filepath.Abs(filepath.Clean(root))
			if err != nil {
				return fmt.Errorf("user %q %s path is invalid", user.ID, label)
			}
			for previous, owner := range rootOwners {
				if pathsOverlap(key, previous) {
					return fmt.Errorf("user %q %s overlaps %s owned by %q", user.ID, label, previous, owner)
				}
			}
			rootOwners[key] = user.ID
		}
		for _, telegramID := range user.TelegramIDs {
			if telegramID <= 0 || seenTelegram[telegramID] {
				return fmt.Errorf("invalid or duplicate Telegram identity %d", telegramID)
			}
			seenTelegram[telegramID] = true
		}
		for _, link := range user.SlackIDs {
			key := link.key()
			if link.TeamID == "" || link.UserID == "" || strings.ContainsAny(key, " \t@") || seenSlack[key] {
				return fmt.Errorf("invalid or duplicate Slack identity %s/%s", link.TeamID, link.UserID)
			}
			seenSlack[key] = true
		}
		seenDisabled := map[string]bool{}
		for _, name := range user.PolicyDisabled {
			if seenDisabled[name] {
				return fmt.Errorf("user %q has duplicate policy-disabled service %q", user.ID, name)
			}
			if _, ok := stack.ServiceInfoByName(name); !ok {
				return fmt.Errorf("user %q has unknown policy-disabled service %q", user.ID, name)
			}
			seenDisabled[name] = true
		}
	}
	if len(c.Users) == 0 {
		return errors.New("at least one user is required")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a, _ = filepath.Abs(filepath.Clean(a))
	b, _ = filepath.Abs(filepath.Clean(b))
	if a == b {
		return true
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// LoadYAML reads a host-owned mapping. Secret values belong in env files.
func LoadYAML(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("one YAML document expected")
	}
	return c, c.Validate()
}

// ConfigFromEnv is the initial single-space deployment path. It uses the
// existing user's runtime roots and maps TELEGRAM_ALLOWED_USERS to that user.
func ConfigFromEnv() (Config, error) {
	if path := os.Getenv("HUB_COMMUNICATION_CONFIG"); path != "" {
		config, err := LoadYAML(path)
		if err != nil {
			return Config{}, err
		}
		config.TelegramToken = os.Getenv("TELEGRAM_BOT_TOKEN")
		if config.APIBaseURL == "" {
			config.APIBaseURL = "https://api.telegram.org"
		}
		if config.SpoolDir == "" {
			config.SpoolDir = envOr("HUB_COMMUNICATION_SPOOL", "/state/gateway")
		}
		config.RuntimeURL = runtimeURLFromEnv()
		config.Supervised = strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")) != ""
		config.RuntimeAuth = runtimeAuthFromEnv()
		for i := range config.Users {
			if config.Users[i].Env == nil {
				config.Users[i].Env = runtimeEnv(config.Users[i].Features)
			}
		}
		config.PollTimeout = 25 * time.Second
		config.HermesCommand = envOr("HUB_HERMES_COMMAND", "hermes")
		config.CredentialStore = os.Getenv("HUB_CREDENTIAL_STORE")
		config.CredentialKeyFile = os.Getenv("HUB_CREDENTIAL_KEY_FILE")
		config.ToolHubStore = os.Getenv("HUB_TOOLHUB_STORE")
		config.AuditLedger = os.Getenv("HUB_AUDIT_LEDGER")
		config.BrokerApprove, err = credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_APPROVE_")
		config.TelegramAuthEnabled = os.Getenv("HUB_TELEGRAM_AUTH_ENABLED") == "true"
		if err != nil {
			return Config{}, err
		}
		config.Workers = workersFromEnv()
		config.SpoolRetention = spoolRetentionFromEnv()
		config.MockDir = os.Getenv("HUB_MOCK_DIR")
		fillChannelSecrets(&config)
		return config, nil
	}
	userID := os.Getenv("HUB_USER_ID")
	if userID == "" {
		userID = "me"
	}
	orgID := os.Getenv("HUB_ORGANIZATION_ID")
	if orgID == "" {
		orgID = "personal"
	}
	ids, err := parseTelegramIDs(os.Getenv("TELEGRAM_ALLOWED_USERS"))
	if err != nil {
		return Config{}, err
	}
	features := splitList(os.Getenv("HUB_ACTIVE_FEATURES"))
	if len(features) == 0 {
		features = splitList(os.Getenv("HUB_FEATURES"))
	}
	configured := map[string]bool{}
	if raw := os.Getenv("HUB_CONFIGURED_ENV"); raw != "" {
		for _, key := range splitList(raw) {
			configured[key] = true
		}
	} else {
		for _, feature := range stack.ServiceCatalog() {
			for _, key := range feature.Requires {
				configured[key] = os.Getenv(key) != ""
			}
		}
	}
	user := User{ID: userID, RuntimeID: envOr("HUB_RUNTIME_ID", userID), PolicyVersion: envOr("HUB_POLICY_VERSION", "policy-1"), Enabled: true, TelegramIDs: ids, SlackIDs: parseSlackLinks(os.Getenv("SLACK_ALLOWED_USERS")), StateDir: envOr("HUB_STATE", "/state"), WorkspaceDir: envOr("HUB_WORKSPACE", "/workspace"), Features: features, ConfiguredEnv: configured, Env: runtimeEnv(features)}
	config := Config{Supervised: strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")) != "", OrganizationID: orgID, Users: []User{user}, TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), APIBaseURL: envOr("TELEGRAM_API_BASE_URL", "https://api.telegram.org"), SpoolDir: envOr("HUB_COMMUNICATION_SPOOL", "/state/gateway"), RuntimeURL: runtimeURLFromEnv(), RuntimeAuth: runtimeAuthFromEnv(), PollTimeout: 25 * time.Second, HermesCommand: envOr("HUB_HERMES_COMMAND", "hermes"), CredentialStore: os.Getenv("HUB_CREDENTIAL_STORE"), CredentialKeyFile: os.Getenv("HUB_CREDENTIAL_KEY_FILE"), ToolHubStore: os.Getenv("HUB_TOOLHUB_STORE"), AuditLedger: os.Getenv("HUB_AUDIT_LEDGER"), Workers: workersFromEnv(), STTTimeout: durationSecondsFromEnv("HUB_STT_TIMEOUT", defaultSTTTimeout), MediaMaxDuration: intFromEnv("HUB_MEDIA_MAX_DURATION", defaultMediaDurationLimit), SpoolRetention: spoolRetentionFromEnv(), MockDir: os.Getenv("HUB_MOCK_DIR")}
	config.BrokerApprove, err = credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_APPROVE_")
	config.TelegramAuthEnabled = os.Getenv("HUB_TELEGRAM_AUTH_ENABLED") == "true"
	if err != nil {
		return Config{}, err
	}
	fillChannelSecrets(&config)
	return config, nil
}

// spoolRetentionFromEnv reads HUB_SPOOL_RETENTION as a duration; the default
// is 336 hours (SPEC-0053 R2.3).
func spoolRetentionFromEnv() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv("HUB_SPOOL_RETENTION"))); err == nil && d > 0 {
		return d
	}
	return 336 * time.Hour
}

// durationSecondsFromEnv reads a seconds-valued duration (HUB_STT_TIMEOUT).
func durationSecondsFromEnv(name string, fallback time.Duration) time.Duration {
	if n := intFromEnv(name, 0); n > 0 {
		return time.Duration(n) * time.Second
	}
	return fallback
}

// intFromEnv reads a positive integer env (HUB_MEDIA_MAX_DURATION).
func intFromEnv(name string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && n > 0 {
		return n
	}
	return fallback
}

// workersFromEnv bounds the job worker pool; default 4 keeps it under the
// supervisor's default runtime cap. Context serialization is unaffected.
func workersFromEnv() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("HUB_COMMUNICATION_WORKERS"))); err == nil && n > 0 {
		return n
	}
	return 4
}

func fillChannelSecrets(config *Config) {
	if config.SlackSigningSecret == "" {
		config.SlackSigningSecret = os.Getenv("SLACK_SIGNING_SECRET")
	}
	if config.SlackBotToken == "" {
		config.SlackBotToken = os.Getenv("SLACK_BOT_TOKEN")
	}
	config.ControlAuth = envOr("HUB_COMMUNICATION_AUTH", config.RuntimeAuth)
	config.ControlTokensFile = os.Getenv("HUB_COMMUNICATION_TOKENS_FILE")
	// The supervisor's own bearer is a control-plane peer too: ToolHub's
	// prepare-outcome posts and sibling runtimes authenticate with it.
	if token := strings.TrimSpace(os.Getenv("HUB_SUPERVISOR_AUTH")); token != "" && token != config.ControlAuth {
		config.controlAuthExtra = append(config.controlAuthExtra, token)
	}
	config.ListenAddr = os.Getenv("HUB_COMMUNICATION_LISTEN")
	config.FormOrigin = os.Getenv("HUB_COMMUNICATION_FORM_ORIGIN")
	config.TelegramAuthAPIID = os.Getenv("HUB_TELEGRAM_AUTH_API_ID")
	config.TelegramAuthAPIHash = os.Getenv("HUB_TELEGRAM_AUTH_API_HASH")
	config.NativeCron = os.Getenv("HUB_NATIVE_CRON")
	config.STTCommand = os.Getenv("HUB_STT_COMMAND")
	config.TTSCommand = os.Getenv("HUB_TTS_COMMAND")
	config.STTURL = os.Getenv("HUB_STT_URL")
	config.STTAuth = envOr("HUB_STT_AUTH", os.Getenv("HUB_MEDIA_AUTH"))
	config.TTSURL = os.Getenv("HUB_TTS_URL")
	config.TTSAuth = envOr("HUB_TTS_AUTH", os.Getenv("HUB_MEDIA_AUTH"))
	config.TTSVoice = os.Getenv("HUB_TTS_VOICE")
	config.TTSUploadURL = os.Getenv("HUB_TTS_UPLOAD_URL")
	config.ToolHubURL = strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_TOOLHUB_URL")), "/")
}

func parseSlackLinks(raw string) []SlackLink {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	result := []SlackLink{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		team, user, ok := strings.Cut(item, "/")
		if !ok || team == "" || user == "" {
			continue
		}
		result = append(result, SlackLink{TeamID: team, UserID: user})
	}
	return result
}

func runtimeURLFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")); value != "" {
		return value
	}
	return os.Getenv("HUB_RUNTIME_URL")
}

func runtimeAuthFromEnv() string {
	if strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")) != "" {
		return os.Getenv("HUB_SUPERVISOR_AUTH")
	}
	return os.Getenv("HUB_RUNTIME_AUTH")
}

func parseTelegramIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("TELEGRAM_ALLOWED_USERS is required")
	}
	seen := map[int64]bool{}
	result := []int64{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if !telegramIDPattern.MatchString(item) {
			return nil, errors.New("TELEGRAM_ALLOWED_USERS must contain numeric IDs")
		}
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil || id <= 0 || seen[id] {
			return nil, errors.New("TELEGRAM_ALLOWED_USERS contains an invalid or duplicate ID")
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitList(raw string) []string {
	result := []string{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func runtimeEnv(features []string) map[string]string {
	keys := map[string]bool{"OPENAI_API_KEY": true}
	if api.Contains(features, "gitlab") {
		keys["GITLAB_HOST"] = true
	}
	if api.Contains(features, "google") {
		keys["GOOGLE_EMAIL"] = true
		keys["GOOGLE_OAUTH_REDIRECT_URI"] = true
	}
	if api.Contains(features, "workspace") || api.Contains(features, "hh") {
		keys["HH_USER_AGENT"] = true
	}
	if api.Contains(features, "slack") {
		keys["SLACK_MCP_ADD_MESSAGE_TOOL"] = true
	}
	for _, feature := range stack.Features {
		if !api.Contains(features, feature.Name) {
			continue
		}
		for _, key := range feature.Requires {
			keys[key] = true
		}
	}
	result := map[string]string{}
	for key := range keys {
		if gatewayOwnedEnv[key] || stack.GatewayOwnedSecret(key) {
			continue
		}
		if value := os.Getenv(key); value != "" {
			result[key] = value
		}
	}
	return result
}
