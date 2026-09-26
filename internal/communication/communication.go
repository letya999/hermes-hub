// Package communication routes channel messages into isolated Hermes jobs.
package communication

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/secrets"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"gopkg.in/yaml.v3"
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
	Supervised         bool                    `yaml:"-"`
	OrganizationID     string                  `yaml:"organization_id"`
	Users              []User                  `yaml:"users"`
	TelegramToken      string                  `yaml:"-"`
	APIBaseURL         string                  `yaml:"api_base_url,omitempty"`
	SlackSigningSecret string                  `yaml:"-"`
	SlackBotToken      string                  `yaml:"-"`
	ControlAuth        string                  `yaml:"-"`
	ListenAddr         string                  `yaml:"-"`
	FormOrigin         string                  `yaml:"-"`
	NativeCron         string                  `yaml:"-"`
	STTCommand         string                  `yaml:"-"`
	TTSCommand         string                  `yaml:"-"`
	TTSUploadURL       string                  `yaml:"-"`
	SpoolDir           string                  `yaml:"spool_dir"`
	RuntimeURL         string                  `yaml:"-"`
	RuntimeAuth        string                  `yaml:"-"`
	PollTimeout        time.Duration           `yaml:"-"`
	HermesCommand      string                  `yaml:"-"`
	CredentialStore    string                  `yaml:"-"`
	CredentialKeyFile  string                  `yaml:"-"`
	ToolHubStore       string                  `yaml:"-"`
	AuditLedger        string                  `yaml:"-"`
	BrokerApprove      credentialbroker.Config `yaml:"-"`
	// Workers bounds concurrent job execution; contexts serialize per
	// (principal_id, context_id), different contexts run in parallel (ADR-0025).
	Workers int `yaml:"-"`
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
		if err != nil {
			return Config{}, err
		}
		config.Workers = workersFromEnv()
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
	config := Config{Supervised: strings.TrimSpace(os.Getenv("HUB_RUNTIME_SUPERVISOR_URL")) != "", OrganizationID: orgID, Users: []User{user}, TelegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"), APIBaseURL: envOr("TELEGRAM_API_BASE_URL", "https://api.telegram.org"), SpoolDir: envOr("HUB_COMMUNICATION_SPOOL", "/state/gateway"), RuntimeURL: runtimeURLFromEnv(), RuntimeAuth: runtimeAuthFromEnv(), PollTimeout: 25 * time.Second, HermesCommand: envOr("HUB_HERMES_COMMAND", "hermes"), CredentialStore: os.Getenv("HUB_CREDENTIAL_STORE"), CredentialKeyFile: os.Getenv("HUB_CREDENTIAL_KEY_FILE"), ToolHubStore: os.Getenv("HUB_TOOLHUB_STORE"), AuditLedger: os.Getenv("HUB_AUDIT_LEDGER"), Workers: workersFromEnv()}
	config.BrokerApprove, err = credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_APPROVE_")
	if err != nil {
		return Config{}, err
	}
	fillChannelSecrets(&config)
	return config, nil
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
	config.ListenAddr = os.Getenv("HUB_COMMUNICATION_LISTEN")
	config.FormOrigin = os.Getenv("HUB_COMMUNICATION_FORM_ORIGIN")
	config.NativeCron = os.Getenv("HUB_NATIVE_CRON")
	config.STTCommand = os.Getenv("HUB_STT_COMMAND")
	config.TTSCommand = os.Getenv("HUB_TTS_COMMAND")
	config.TTSUploadURL = os.Getenv("HUB_TTS_UPLOAD_URL")
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
	if contains(features, "gitlab") {
		keys["GITLAB_HOST"] = true
	}
	if contains(features, "google") {
		keys["GOOGLE_EMAIL"] = true
		keys["GOOGLE_OAUTH_REDIRECT_URI"] = true
	}
	if contains(features, "workspace") || contains(features, "hh") {
		keys["HH_USER_AGENT"] = true
	}
	if contains(features, "slack") {
		keys["SLACK_MCP_ADD_MESSAGE_TOOL"] = true
	}
	for _, feature := range stack.Features {
		if !contains(features, feature.Name) {
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

// Job is immutable routing context plus the user message. Sensitive prompts are
// held only in memory by Spool and are intentionally absent from job files.
type Job struct {
	identity.Envelope
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	UserID         string    `json:"user_id"`
	ActorID        string    `json:"actor_id"`
	ScopeID        string    `json:"scope_id"`
	Channel        string    `json:"channel"`
	Trigger        string    `json:"trigger"`
	IdempotencyKey string    `json:"idempotency_key"`
	ChatID         int64     `json:"chat_id"`
	MessageID      int       `json:"message_id"`
	SlackChannel   string    `json:"slack_channel,omitempty"`
	SlackThread    string    `json:"slack_thread,omitempty"`
	Text           string    `json:"text,omitempty"`
	Sensitive      bool      `json:"sensitive"`
	TextSHA256     string    `json:"text_sha256,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Delivery struct {
	ID               string    `json:"id"`
	JobID            string    `json:"job_id"`
	IdempotencyKey   string    `json:"idempotency_key,omitempty"`
	ConversationID   string    `json:"conversation_id"`
	DeliveryTargetID string    `json:"delivery_target_id"`
	Channel          string    `json:"channel,omitempty"`
	ChatID           int64     `json:"chat_id"`
	SlackChannel     string    `json:"slack_channel,omitempty"`
	SlackThread      string    `json:"slack_thread,omitempty"`
	Text             string    `json:"text"`
	Attempts         int       `json:"attempts"`
	CreatedAt        time.Time `json:"created_at"`
}

// Spool is a tiny durable queue for one host. Renames are its state machine.
type Spool struct {
	root   string
	mu     sync.Mutex
	secret map[string]string
	// inFlight maps (principal, context) to the job currently allowed to run;
	// held until the job's mapping reaches a terminal status. In-memory only,
	// rebuilt from durable mappings on startup (ADR-0025).
	inFlight map[string]string
}

func NewSpool(root string) (*Spool, error) {
	s := &Spool{root: root, secret: map[string]string{}, inFlight: map[string]string{}}
	for _, dir := range []string{"pending", "running", "done", "failed", "outbox/pending", "outbox/sending", "outbox/done", "outbox/failed", "mappings", "conversations", "events", "occurrences", "schedules", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	// A process can die after claiming work. Mapped jobs stay failed/uncertain so
	// a restart never silently replays an external run; legacy jobs remain retryable.
	if err := s.recoverJobs(); err != nil {
		return nil, err
	}
	if err := recoverFiles(filepath.Join(root, "outbox", "sending"), filepath.Join(root, "outbox", "failed")); err != nil {
		return nil, err
	}
	if err := s.rebuildMappings(); err != nil {
		return nil, err
	}
	if err := s.recoverStreamEvents(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Spool) recoverJobs() error {
	from, to := filepath.Join(s.root, "running"), filepath.Join(s.root, "pending")
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		jobID := strings.TrimSuffix(name, ".json")
		mapping, loadErr := s.loadMappingLocked(jobID)
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			return loadErr // Corrupt admission metadata must never make a claimed job retryable.
		}
		if loadErr == nil {
			if terminalStatus(mapping.Status) {
				dir := "failed"
				if mapping.Status == "completed" {
					dir = "done"
				}
				if err := os.Rename(filepath.Join(from, name), filepath.Join(s.root, dir, name)); err != nil {
					return err
				}
				continue
			}
			mapping.Status, mapping.LastKnownEvent, mapping.UpdatedAt = "uncertain", "run.unknown", time.Now().UTC()
			if err := s.writeMappingLocked(mapping); err != nil {
				return err
			}
			destination := "failed"
			if mapping.RunID != "" && mapping.SessionID != "" {
				destination = "pending"
			} // Observe the admitted run; never resubmit its prompt.
			if err := os.Rename(filepath.Join(from, name), filepath.Join(s.root, destination, name)); err != nil {
				return err
			}
			continue
		}
		if err := os.Rename(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) rebuildMappings() error {
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		entries, err := os.ReadDir(filepath.Join(s.root, dir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(s.root, dir, entry.Name()))
			if err != nil {
				return err
			}
			var job Job
			if json.Unmarshal(body, &job) != nil || job.IdempotencyKey == "" {
				continue
			}
			if _, err := os.Stat(s.mappingPath(job.ID)); errors.Is(err, os.ErrNotExist) {
				mapping := mappingFromJob(job, job.CreatedAt.UTC())
				mapping.JobID = job.ID
				mapping.Status = map[string]string{"pending": "accepted", "running": "running", "done": "completed", "failed": "failed"}[dir]
				if err := s.writeMappingLocked(mapping); err != nil {
					return err
				}
			}
			if mapping, err := s.loadMappingLocked(job.ID); err == nil && mappingHoldsContext(mapping) {
				s.inFlight[jobContextKey(mapping.PrincipalID, mapping.ContextID)] = job.ID
			}
		}
	}
	return nil
}

func recoverFiles(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if err := os.Rename(filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) Enqueue(job Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enqueueLocked(job)
}
func (s *Spool) enqueueLocked(job Job) (bool, error) {
	fingerprint := jobFingerprint(job)
	if strings.TrimSpace(job.IdempotencyKey) != "" {
		if mapping, found, err := s.findIdempotencyLocked(job.IdempotencyKey); err != nil {
			return false, err
		} else if found {
			if mapping.Fingerprint != jobFingerprint(job) {
				return false, errors.New("idempotency key already used with different payload")
			}
			return false, nil
		}
	}
	for _, dir := range []string{"pending", "running", "done", "failed"} {
		if _, err := os.Stat(filepath.Join(s.root, dir, spoolFileID(job.ID)+".json")); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if job.Sensitive {
		s.secret[job.ID] = job.Text
		h := sha256.Sum256([]byte(job.Text))
		job.TextSHA256 = hex.EncodeToString(h[:])
		job.Text = ""
	}
	if err := atomicJSON(filepath.Join(s.root, "pending", spoolFileID(job.ID)+".json"), job); err != nil {
		return false, err
	}
	if strings.TrimSpace(job.IdempotencyKey) != "" {
		mapping := mappingFromJob(job, job.CreatedAt.UTC())
		mapping.Fingerprint = fingerprint
		if err := s.writeMappingLocked(mapping); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Spool) findIdempotencyLocked(key string) (JobMapping, bool, error) {
	// ponytail: linear scan is bounded by the single-host spool; add an index if job volume makes it measurable.
	entries, err := os.ReadDir(filepath.Join(s.root, "mappings"))
	if err != nil {
		return JobMapping{}, false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.root, "mappings", entry.Name()))
		if err != nil {
			return JobMapping{}, false, err
		}
		var mapping JobMapping
		if err := json.Unmarshal(b, &mapping); err != nil || mapping.JobID == "" {
			return JobMapping{}, false, errors.New("invalid job mapping")
		}
		if mapping.IdempotencyKey == key {
			return mapping, true, nil
		}
	}
	return JobMapping{}, false, nil
}

func (s *Spool) ClaimJob() (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight == nil {
		s.inFlight = map[string]string{}
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "pending"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := entry.Name()
		pending := filepath.Join(s.root, "pending", name)
		b, err := os.ReadFile(pending)
		if err != nil {
			return nil, err
		}
		var job Job
		if err := json.Unmarshal(b, &job); err != nil {
			return nil, err
		}
		key := jobContextKey(job.PrincipalID, job.ContextID)
		if holder, busy := s.inFlight[key]; busy && holder != job.ID {
			continue // Context serialization is per key; other contexts stay claimable.
		}
		to := filepath.Join(s.root, "running", name)
		if err := os.Rename(pending, to); err != nil {
			return nil, err
		}
		mapping, mappingErr := s.loadMappingLocked(job.ID)
		if mappingErr != nil && job.IdempotencyKey != "" {
			// Routing and cancellation state must be readable before executing work.
			return nil, mappingErr
		}
		if mappingErr == nil && terminalStatus(mapping.Status) {
			dir := "failed"
			if mapping.Status == "completed" {
				dir = "done"
			}
			if err := os.Rename(to, filepath.Join(s.root, dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if mappingErr == nil && mapping.RunID != "" && mapping.SessionID != "" {
			job.Text = ""
		} else if job.Sensitive {
			job.Text = s.secret[job.ID]
			if job.Text == "" {
				_ = os.Rename(to, filepath.Join(s.root, "failed", name))
				return nil, errors.New("sensitive job payload unavailable after restart")
			}
		}
		if strings.TrimSpace(job.IdempotencyKey) != "" {
			if err := s.updateMappingLocked(job.ID, RunOutcome{JobID: job.ID, Status: "running", LastEvent: "job.claimed"}, "running", false); err != nil {
				return nil, err
			}
		}
		s.inFlight[key] = job.ID
		return &job, nil
	}
	return nil, nil
}

func (s *Spool) CompleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseInFlightLocked(id)
	if err := os.Rename(filepath.Join(s.root, "running", spoolFileID(id)+".json"), filepath.Join(s.root, "done", spoolFileID(id)+".json")); err != nil {
		return err
	}
	if _, err := s.loadMappingLocked(id); err == nil {
		return s.updateMappingLocked(id, RunOutcome{Status: "completed", LastEvent: "job.completed"}, "completed", true)
	}
	return nil
}

func (s *Spool) FailJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secret, id)
	fileID := spoolFileID(id)
	err := os.Rename(filepath.Join(s.root, "running", fileID+".json"), filepath.Join(s.root, "failed", fileID+".json"))
	if mapping, loadErr := s.loadMappingLocked(id); loadErr == nil {
		// terminal outcomes release via updateMappingLocked; uncertain holds.
		status := "failed"
		if mapping.Status == "uncertain" || mapping.Status == "cancelled" || mapping.Status == "interrupted" {
			status = "uncertain"
			if mapping.Status != "uncertain" {
				status = mapping.Status
			}
		}
		if updateErr := s.updateMappingLocked(id, RunOutcome{Status: status, LastEvent: "job." + status}, status, status != "uncertain"); err == nil {
			err = updateErr
		}
	} else {
		s.releaseInFlightLocked(id)
	}
	return err
}

func (s *Spool) EnqueueDelivery(d Delivery) error {
	if d.Channel == "" {
		d.Channel = "telegram_bot"
	}
	switch d.Channel {
	case "telegram_bot":
		transportID := "telegram-" + strconv.FormatInt(d.ChatID, 10)
		if d.ConversationID == "" {
			d.ConversationID = transportID
		}
		if d.DeliveryTargetID == "" {
			d.DeliveryTargetID = transportID
		}
		if d.ChatID <= 0 || !identity.ValidID(d.ConversationID) || d.DeliveryTargetID != transportID {
			return errors.New("invalid delivery identity")
		}
	case "slack_app":
		if !identity.ValidID(d.ConversationID) || !identity.ValidID(d.DeliveryTargetID) || d.DeliveryTargetID != d.ConversationID || !validSlackIMChannel(d.SlackChannel) || !validSlackThread(d.SlackThread) {
			return errors.New("invalid delivery identity")
		}
	default:
		return errors.New("invalid delivery identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, dir := range []string{"pending", "sending", "done", "failed"} {
		if _, err := os.Stat(filepath.Join(s.root, "outbox", dir, spoolFileID(d.ID)+".json")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return atomicJSON(filepath.Join(s.root, "outbox", "pending", spoolFileID(d.ID)+".json"), d)
}

func (s *Spool) LoadOffset() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(filepath.Join(s.root, "offset.json"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var value struct {
		Offset int64 `json:"offset"`
	}
	if err := json.Unmarshal(b, &value); err != nil || value.Offset < 0 {
		return 0, errors.New("invalid Telegram update offset")
	}
	return value.Offset, nil
}

func (s *Spool) SaveOffset(offset int64) error {
	if offset < 0 {
		return errors.New("negative Telegram update offset")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicJSON(filepath.Join(s.root, "offset.json"), struct {
		Offset int64 `json:"offset"`
	}{offset})
}

func (s *Spool) ClaimDelivery() (*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, err := firstJSON(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || name == "" {
		return nil, err
	}
	from, to := filepath.Join(s.root, "outbox", "pending", name), filepath.Join(s.root, "outbox", "sending", name)
	if err := os.Rename(from, to); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(to)
	if err != nil {
		return nil, err
	}
	var d Delivery
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Spool) CompleteDelivery(id string) error { return s.move("outbox/sending", "outbox/done", id) }
func (s *Spool) UncertainDelivery(id string) error {
	return s.move("outbox/sending", "outbox/failed", id)
}

func (s *Spool) move(from, to, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fileID := spoolFileID(id)
	return os.Rename(filepath.Join(s.root, from, fileID+".json"), filepath.Join(s.root, to, fileID+".json"))
}

func spoolFileID(id string) string {
	if spoolIDPattern.MatchString(id) {
		return id
	}
	hash := sha256.Sum256([]byte(id))
	return "id-" + hex.EncodeToString(hash[:])
}

func firstJSON(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".queue-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Telegram API subset used by the adapter.
type Update struct {
	UpdateID      int      `json:"update_id"`
	Message       *Message `json:"message,omitempty"`
	EditedMessage *Message `json:"edited_message,omitempty"`
}
type Message struct {
	MessageID int       `json:"message_id"`
	From      *TGUser   `json:"from,omitempty"`
	Chat      TGChat    `json:"chat"`
	Text      string    `json:"text,omitempty"`
	Caption   string    `json:"caption,omitempty"`
	Voice     *TGMedia  `json:"voice,omitempty"`
	Audio     *TGMedia  `json:"audio,omitempty"`
	Document  *TGMedia  `json:"document,omitempty"`
	Photo     []TGMedia `json:"photo,omitempty"`
}
type TGUser struct {
	ID int64 `json:"id"`
}
type TGChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type TGMedia struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration,omitempty"`
}
type TelegramFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

type TelegramAPI interface {
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, string) error
	DeleteMessage(context.Context, int64, int) error
	SendChatAction(context.Context, int64, string) error
	GetFile(context.Context, string) (TelegramFile, error)
	DownloadFile(context.Context, string, io.Writer) error
	SendVoice(context.Context, int64, []byte, string) error
}

type telegramAPI struct {
	baseURL string
	token   string
	client  *http.Client
}

func newTelegramAPI(baseURL, token string, timeout time.Duration) TelegramAPI {
	if timeout <= 0 {
		timeout = 35 * time.Second
	}
	return &telegramAPI{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{Timeout: timeout}}
}

func (t *telegramAPI) call(ctx context.Context, method string, payload any, result any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.callRequest(req, result)
}

func (t *telegramAPI) callRequest(req *http.Request, result any) error {
	response, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("telegram API returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return errors.New("telegram API request failed")
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

func (t *telegramAPI) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var result []Update
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "edited_message"}}, &result)
	return result, err
}
func (t *telegramAPI) SendMessage(ctx context.Context, chatID int64, text string) error {
	if !utf8.ValidString(text) || text == "" || len(text) > 2*1024*1024 {
		return errors.New("invalid Telegram output")
	}
	if utf8.RuneCountInString(text) > 4096 {
		// Preserve the complete bounded result in one delivery, without a
		// partially sent multi-message sequence that cannot safely be retried.
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
			return err
		}
		part, err := form.CreateFormFile("document", "response.txt")
		if err != nil {
			return err
		}
		if _, err := io.WriteString(part, text); err != nil {
			return err
		}
		if err := form.Close(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendDocument", &body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", form.FormDataContentType())
		return t.callRequest(req, nil)
	}
	return t.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
}
func (t *telegramAPI) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return t.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}
func (t *telegramAPI) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return t.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}
func (t *telegramAPI) GetFile(ctx context.Context, fileID string) (TelegramFile, error) {
	var file TelegramFile
	err := t.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file)
	return file, err
}
func (t *telegramAPI) DownloadFile(ctx context.Context, filePath string, dest io.Writer) error {
	if filePath == "" || strings.Contains(filePath, "..") {
		return errors.New("invalid telegram file path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/file/bot"+t.token+"/"+filePath, nil)
	if err != nil {
		return err
	}
	response, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("telegram file download returned HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(dest, io.LimitReader(response.Body, mediaSizeLimit+1))
	return err
}
func (t *telegramAPI) SendVoice(ctx context.Context, chatID int64, audio []byte, caption string) error {
	if len(audio) == 0 || len(audio) > mediaSizeLimit {
		return errors.New("invalid voice payload")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if caption != "" {
		if err := form.WriteField("caption", limitTelegramText(caption)); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("voice", "reply.ogg")
	if err != nil {
		return err
	}
	if _, err := part.Write(audio); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendVoice", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return t.callRequest(req, nil)
}

type Runner interface {
	Run(context.Context, Job, User) (string, error)
}

type outcomeRunner interface {
	RunOutcome(context.Context, Job) (RunOutcome, error)
}

type HermesRunner struct {
	Command string
	Timeout time.Duration
}

func (r HermesRunner) Run(ctx context.Context, job Job, user User) (string, error) {
	if job.Text == "" {
		return "", errors.New("empty Hermes prompt")
	}
	if r.Command == "" {
		r.Command = "hermes"
	}
	if r.Timeout <= 0 {
		r.Timeout = 120 * time.Second
	}
	if err := os.MkdirAll(user.StateDir, 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(user.WorkspaceDir, 0700); err != nil {
		return "", err
	}
	jobCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, r.Command, "-z", job.Text)
	cmd.Dir = user.WorkspaceDir
	cmd.Env = processEnv(user.Env)
	cmd.Env = append(cmd.Env,
		"HERMES_HOME="+filepath.Join(user.StateDir, "hermes"),
		"HOME="+filepath.Join(user.StateDir, "home"),
		"HUB_STATE="+user.StateDir,
		"HUB_WORKSPACE="+user.WorkspaceDir,
		"HUB_USER_ID="+user.ID,
		"HUB_ORGANIZATION_ID="+job.OrganizationID,
		"HUB_FEATURES="+strings.Join(user.Features, ","),
		"HUB_DEFER_RUNTIME_RESTART=true",
	)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	if err := cmd.Run(); err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("hermes timed out")
		}
		return "", errors.New("hermes invocation failed")
	}
	response := strings.TrimSpace(stdout.String())
	if response == "" {
		return "", errors.New("hermes returned an empty response")
	}
	return response, nil
}

func processEnv(userEnv map[string]string) []string {
	env := []string{}
	for _, key := range []string{"PATH", "Path", "SystemRoot", "TEMP", "TMP", "LANG", "LC_ALL", "TZ", "HUB_ORG_ACTIONS", "HUB_SELF_ENV_KEYS", "HUB_PROTECTED_ENV_KEYS"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	keys := make([]string, 0, len(userEnv))
	for key := range userEnv {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if envNamePattern.MatchString(key) && !gatewayOwnedEnv[key] && !stack.GatewayOwnedSecret(key) {
			env = append(env, key+"="+userEnv[key])
		}
	}
	return env
}

type Gateway struct {
	config      Config
	users       map[int64]User
	slackUsers  map[string]User
	spool       *Spool
	api         TelegramAPI
	slack       SlackAPI
	runner      Runner
	restart     func(context.Context, hubruntime.ExecuteRequest) error
	busy        atomic.Int32
	now         func() time.Time
	secrets     *secrets.Service
	audit       *audit.Ledger
	transcriber Transcriber
	synthesizer Synthesizer
	formsMu     sync.Mutex
	forms       map[string]credentialForm
}

func New(config Config) (*Gateway, error) {
	if config.PollTimeout <= 0 {
		config.PollTimeout = 25 * time.Second
	}
	if config.HermesCommand == "" {
		config.HermesCommand = "hermes"
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.TelegramToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	for _, user := range config.Users {
		if !user.Enabled {
			continue
		}
		if config.RuntimeURL != "" {
			continue
		}
		for _, root := range []string{user.StateDir, user.WorkspaceDir} {
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() {
				return nil, fmt.Errorf("user %q runtime root is unavailable", user.ID)
			}
		}
	}
	spool, err := NewSpool(config.SpoolDir)
	if err != nil {
		return nil, err
	}
	users := map[int64]User{}
	slackUsers := map[string]User{}
	for _, user := range config.Users {
		if !user.Enabled {
			continue
		}
		for _, id := range user.TelegramIDs {
			users[id] = user
		}
		for _, link := range user.SlackIDs {
			slackUsers[link.key()] = user
		}
	}
	runner := Runner(HermesRunner{Command: config.HermesCommand})
	if config.RuntimeURL != "" {
		runner = HTTPRunner{URL: config.RuntimeURL, Auth: config.RuntimeAuth, Spool: spool, JobsAPI: config.Supervised}
	}
	restart := runtimeRestart(config.RuntimeURL, config.RuntimeAuth, config.Supervised)
	secretService, ledger, err := openCredentialSurface(config)
	if err != nil {
		return nil, err
	}
	g := &Gateway{config: config, users: users, slackUsers: slackUsers, spool: spool, api: newTelegramAPI(config.APIBaseURL, config.TelegramToken, config.PollTimeout+10*time.Second), runner: runner, restart: restart, now: time.Now, secrets: secretService, audit: ledger, transcriber: commandTranscriber(config.STTCommand), synthesizer: commandSynthesizer(config.TTSCommand), forms: map[string]credentialForm{}}
	if config.SlackBotToken != "" {
		g.slack = newSlackAPI(config.SlackBotToken)
	}
	return g, nil
}

func openCredentialSurface(config Config) (*secrets.Service, *audit.Ledger, error) {
	if config.BrokerApprove.Enabled() {
		return nil, nil, nil
	}
	if config.CredentialStore == "" {
		return nil, nil, nil
	}
	opened, err := secrets.Open(config.CredentialStore, config.CredentialKeyFile, nil, "")
	if err != nil {
		return nil, nil, err
	}
	auditPath := config.AuditLedger
	if auditPath == "" {
		auditPath = filepath.Join(filepath.Dir(config.CredentialStore), "audit.jsonl")
	}
	ledger, err := audit.Open(auditPath)
	if err != nil {
		return nil, nil, err
	}
	opened.Audit = ledger
	opened.EnvFile = func(owner string) string {
		for _, user := range config.Users {
			if user.ID == owner {
				return filepath.Join(user.StateDir, envstore.FileName)
			}
		}
		return ""
	}
	storePath := config.ToolHubStore
	if storePath == "" && len(config.Users) > 0 && config.Users[0].StateDir != "" {
		candidate := filepath.Join(config.Users[0].StateDir, "runtime", "toolhub", "store.json")
		if _, err := os.Lstat(candidate); err == nil {
			storePath = candidate
		}
	}
	if storePath != "" {
		if _, err := os.Lstat(storePath); err == nil {
			registry, err := toolhub.Load(storePath)
			if err != nil {
				return nil, nil, err
			}
			opened.Registry = registry
		} else if !os.IsNotExist(err) {
			return nil, nil, err
		}
	}
	return opened, ledger, nil
}

func (g *Gateway) Run(ctx context.Context) error {
	workerCtx, cancel := context.WithCancel(ctx)
	var workerWG sync.WaitGroup
	for i := 0; i < max(1, g.config.Workers); i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			g.worker(workerCtx)
		}()
	}
	deliveryDone := make(chan struct{})
	controlDone := make(chan struct{})
	go func() {
		defer close(controlDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			now := g.now().UTC()
			_ = g.spool.TickSchedules(now, g.config.NativeCron, g.authorizeOccurrence)
			_ = g.spool.DispatchDueOccurrences(now, g.authorizeOccurrence)
			g.controlOne(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(deliveryDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			g.deliverOne(workerCtx)
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() {
		cancel()
		workerWG.Wait()
		<-deliveryDone
		<-controlDone
	}()
	offset, err := g.spool.LoadOffset()
	if err != nil {
		return err
	}
	for {
		updates, err := g.api.GetUpdates(ctx, offset, int(g.config.PollTimeout/time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(time.Second)
			continue
		}
		for _, update := range updates {
			if err := g.handleUpdate(ctx, update); err != nil {
				return err
			}
			if int64(update.UpdateID) >= offset {
				offset = int64(update.UpdateID) + 1
				if err := g.spool.SaveOffset(offset); err != nil {
					return err
				}
			}
		}
	}
}

func (g *Gateway) handleUpdate(ctx context.Context, update Update) error {
	edited := update.EditedMessage != nil
	message := update.Message
	if message == nil {
		message = update.EditedMessage
	}
	if message == nil || message.From == nil {
		return nil
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		text = strings.TrimSpace(message.Caption)
	}
	if message.Chat.Type != "private" {
		if envstore.LooksLikeEnv(text) {
			if _, ok := g.users[message.From.ID]; ok {
				_ = g.api.DeleteMessage(ctx, message.Chat.ID, message.MessageID)
				return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-group-secret", message.Chat.ID, "Группы не принимают секреты.")
			}
		}
		return nil
	}
	user, ok := g.users[message.From.ID]
	if !ok {
		if text == "" && message.Voice == nil && message.Audio == nil && message.Document == nil && len(message.Photo) == 0 {
			return nil
		}
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, "Доступ к этому боту не настроен.")
	}
	if edited {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-edit", message.Chat.ID, "Изменение сообщения не перезапускает задачу. Используйте новое сообщение или /cancel.")
	}
	if strings.HasPrefix(text, "/") {
		command := strings.ToLower(strings.TrimPrefix(strings.Fields(text)[0], "/"))
		if at := strings.IndexByte(command, '@'); at >= 0 {
			command = command[:at]
		}
		switch command {
		case "runat":
			fields := strings.Fields(text)
			answer := "Используйте /runat <дата RFC3339 с часовым поясом> <задание>."
			if len(fields) >= 3 {
				due, err := time.Parse(time.RFC3339, fields[1])
				if err == nil && due.After(g.now()) {
					input := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, fields[0])), fields[1]))
					caller := user.envelope(message.From.ID)
					job := Job{Envelope: caller, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", ChatID: message.Chat.ID, Text: input}
					err = g.spool.PutOccurrence(RoutineOccurrence{ScheduleID: "telegram-" + strconv.Itoa(update.UpdateID), Revision: 1, DueAt: due, Job: job}, caller)
					if err == nil {
						answer = "Задание по расписанию сохранено."
					} else {
						answer = "Задание отклонено: проверьте текст и время."
					}
				}
			}
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, answer)
		case "cancel", "approve":
			fields := strings.Fields(text)
			caller := user.envelope(message.From.ID)
			answer := "Используйте /cancel <job-id> или /approve <job-id> <request-id> <choice>."
			var err error
			if command == "cancel" && len(fields) == 2 {
				_, err = g.spool.RequestCancel(fields[1], caller)
				answer = "Запрос отмены сохранен."
			} else if command == "approve" && len(fields) == 4 {
				err = g.spool.RequestApproval(fields[1], caller, fields[2], fields[3])
				answer = "Ответ на подтверждение сохранен."
			}
			if err != nil {
				answer = "Запрос отклонен: задача или подтверждение недоступны в этом разговоре."
			}
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, answer)
		case "credentials", "broker-approve":
			fields := strings.Fields(text)
			answer := "Используйте /credentials <request-id> <код из формы Credential Broker>."
			if len(fields) == 3 && g.config.BrokerApprove.Enabled() {
				caller := user.envelope(message.From.ID)
				client, err := g.config.BrokerApprove.New(caller, "broker:approve")
				if err == nil {
					err = client.Approve(ctx, fields[1], fields[2])
				}
				if err == nil {
					answer = "Подтверждение Credential Broker сохранено. Вернитесь в Hermes и запросите статус подключения."
				} else {
					answer = "Подтверждение отклонено или истекло. Запросите новую ссылку подключения."
				}
			}
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, answer)
		case "start":
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, "Готово. Вы подключены к своему Hermes-пространству.")
		case "status":
			state := "свободен"
			if g.busy.Load() > 0 {
				state = "обрабатывает сообщение"
			}
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, "Hermes: "+state+".")
		case "connections", "connection":
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, connectionList(user))
		case "session":
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, g.sessionStatus(user, message.From.ID))
		case "voice":
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, g.voiceCommand(user, message.From.ID, text))
		case "routine":
			return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-reply", message.Chat.ID, g.routineCommand(user, message.From.ID, message.Chat.ID, text))
		}
	}
	if message.Voice != nil || message.Audio != nil {
		return g.handleVoice(ctx, update, user, message)
	}
	if message.Document != nil || len(message.Photo) > 0 {
		return g.handleFile(ctx, update, user, message)
	}
	if text == "" {
		return nil
	}
	if envstore.LooksLikeEnv(text) {
		return g.interceptSecret(ctx, user, update.UpdateID, message.Chat.ID, message.MessageID, text)
	}
	return g.enqueueChannelJob(ctx, user, "telegram_bot", "telegram-"+strconv.Itoa(update.UpdateID), "telegram:"+strconv.Itoa(update.UpdateID), message.Chat.ID, message.MessageID, text, user.envelope(message.From.ID))
}

func (g *Gateway) enqueueChannelJob(ctx context.Context, user User, channel, id, idempotency string, chatID int64, messageID int, text string, envelope identity.Envelope) error {
	job := Job{Envelope: envelope, ID: id, OrganizationID: g.config.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: channel, Trigger: "message", IdempotencyKey: idempotency, ChatID: chatID, MessageID: messageID, Text: text, Sensitive: false, CreatedAt: g.now().UTC()}
	if _, err := g.spool.Enqueue(job); err != nil {
		return err
	}
	if channel == "telegram_bot" {
		_ = g.api.SendChatAction(ctx, chatID, "typing")
	}
	return nil
}

func (g *Gateway) sessionStatus(user User, sender int64) string {
	conversation := user.envelope(sender).ConversationID
	mapping, found, err := g.spool.SessionFor(user.ID, user.ID, conversation)
	if err != nil || !found || mapping.SessionID == "" {
		return "Постоянная Hermes-сессия для этого разговора ещё не создана."
	}
	return "Сессия: " + mapping.SessionID + "."
}

func (g *Gateway) voiceCommand(user User, sender int64, text string) string {
	fields := strings.Fields(text)
	conversation := user.envelope(sender).ConversationID
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "on", "enable":
			if err := g.spool.SetConversationVoice(user.ID, user.ID, conversation, true); err != nil {
				return "Не удалось включить голосовые ответы."
			}
			return "Голосовые ответы включены для этого разговора. Текст остаётся канонической записью."
		case "off", "disable":
			if err := g.spool.SetConversationVoice(user.ID, user.ID, conversation, false); err != nil {
				return "Не удалось выключить голосовые ответы."
			}
			return "Голосовые ответы выключены."
		}
	}
	enabled, _ := g.spool.ConversationVoice(user.ID, user.ID, conversation)
	if enabled {
		return "Голосовые ответы включены. Каноническая доставка — текст. /voice off чтобы выключить."
	}
	return "Голосовые ответы выключены по умолчанию. /voice on чтобы включить для этого разговора."
}

func (g *Gateway) interceptSecret(ctx context.Context, user User, updateID int, chatID int64, messageID int, text string) error {
	_ = g.api.DeleteMessage(ctx, chatID, messageID)
	reply := "Секреты через чат не принимаются."
	values, err := envstore.Parse(text)
	if err == nil && len(values) > 0 && len(user.TelegramIDs) > 0 {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
			values[key] = ""
		}
		slices.Sort(keys)
		if form, formErr := g.createCredentialForm(user.envelope(user.TelegramIDs[0]), keys); formErr == nil {
			reply = "Введите данные только в защищённой форме: " + form.FormURL
		}
	}
	return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(updateID)+"-secret", chatID, reply)
}

func (g *Gateway) queueDelivery(_ context.Context, key string, chatID int64, text string) error {
	id := key
	if id == "" {
		id = newID()
	}
	return g.spool.EnqueueDelivery(Delivery{ID: id, IdempotencyKey: key, Channel: "telegram_bot", ChatID: chatID, Text: text, CreatedAt: g.now().UTC()})
}

func (g *Gateway) worker(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		g.deliverOne(ctx)
		if g.config.RuntimeURL != "" {
			_ = g.spool.RequeueObservations(g.now().UTC())
		}
		if job, err := g.spool.ClaimJob(); err == nil && job != nil {
			g.busy.Add(1)
			var response string
			var outcome RunOutcome
			var runErr error
			if detailed, ok := g.runner.(outcomeRunner); ok {
				outcome, runErr = detailed.RunOutcome(ctx, *job)
				response = outcome.Text
			} else {
				response, runErr = g.runner.Run(ctx, *job, g.user(job.UserID))
				outcome = RunOutcome{Text: response, Status: "completed", LastEvent: "run.completed"}
			}
			g.busy.Add(-1)
			if runErr != nil || outcome.Status == "uncertain" {
				if mapping, found, err := g.spool.Mapping(job.ID); err == nil && found && terminalStatus(mapping.Status) {
					outcome = RunOutcome{Text: mapping.Result, JobID: mapping.JobID, RunID: mapping.RunID, SessionID: mapping.SessionID, RuntimeGeneration: mapping.RuntimeGeneration, Status: mapping.Status, LastEvent: mapping.LastKnownEvent}
					response = outcome.Text
					if mapping.Status == "completed" {
						runErr = nil
					} else {
						runErr = errors.New("run ended without completion")
					}
				}
			}
			if runErr != nil || outcome.Status == "uncertain" {
				if runErr == nil {
					runErr = ErrUncertain
				}
				if outcome.Status != "" && outcome.Status != "uncertain" {
					_ = g.spool.RecordOutcome(job.ID, outcome)
				}
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					_ = g.spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, SessionID: outcome.SessionID, RunID: outcome.RunID, RuntimeGeneration: outcome.RuntimeGeneration, Status: "uncertain", LastEvent: "run.unknown"})
				}
				_ = g.spool.FailJob(job.ID)
				key, message := "job-"+job.ID+"-error", "Не удалось завершить задачу."
				if errors.Is(runErr, ErrUncertain) || outcome.Status == "uncertain" {
					key = "job-" + job.ID + "-uncertain"
					message = "Исход задачи пока неизвестен. Повторный запуск может продублировать действие. ID: " + job.ID
					if outcome.RunID != "" && g.config.RuntimeURL != "" {
						message = "Проверяю исходную задачу после потери соединения. ID: " + job.ID
					}
				} else if outcome.Status == "cancelled" {
					message = "Задача отменена. ID: " + job.ID
				}
				_ = g.spool.EnqueueDelivery(Delivery{ID: key, IdempotencyKey: key, JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: message, CreatedAt: g.now().UTC()})
			} else {
				_ = g.spool.RecordOutcome(job.ID, outcome)
				_ = g.spool.EnqueueDelivery(Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, SlackChannel: job.SlackChannel, SlackThread: job.SlackThread, Text: response, CreatedAt: g.now().UTC()})
				_ = g.spool.CompleteJob(job.ID)
			}
			g.recordJob(*job, outcome)
			if job.Sensitive {
				_ = g.api.DeleteMessage(ctx, job.ChatID, job.MessageID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (g *Gateway) deliverOne(ctx context.Context) {
	delivery, err := g.spool.ClaimDelivery()
	if err != nil || delivery == nil {
		return
	}
	delivery.Attempts++
	var sendErr error
	switch delivery.Channel {
	case "slack_app":
		if g.slack == nil {
			sendErr = errors.New("slack delivery is not configured")
			break
		}
		sendErr = g.slack.PostMessage(ctx, delivery.SlackChannel, delivery.SlackThread, delivery.Text)
	default:
		sendErr = g.api.SendMessage(ctx, delivery.ChatID, limitTelegramText(delivery.Text))
		if sendErr == nil {
			g.maybeSendVoice(ctx, *delivery)
		}
	}
	if sendErr != nil {
		// A timeout may have sent the message. Keep it failed so a restart never
		// blindly duplicates an uncertain Telegram delivery.
		_ = g.spool.UncertainDelivery(delivery.ID)
		return
	}
	_ = g.spool.CompleteDelivery(delivery.ID)
	if delivery.JobID != "" {
		if g.restart != nil {
			request, ok := g.restartRequest(*delivery)
			if ok {
				_ = g.restart(ctx, request)
			}
		} else {
			user := g.userByChatID(delivery.ChatID)
			if user.StateDir != "" {
				_ = signalRestartAfterDelivery(user.StateDir)
			}
		}
	}
}

func (g *Gateway) maybeSendVoice(ctx context.Context, delivery Delivery) {
	if delivery.JobID == "" || delivery.Channel == "slack_app" {
		return
	}
	user := g.userByChatID(delivery.ChatID)
	if user.ID == "" {
		return
	}
	enabled, err := g.spool.ConversationVoice(user.ID, user.ID, delivery.ConversationID)
	if err != nil || !enabled || g.synthesizer == nil {
		return
	}
	audio, _, synthErr := g.synthesizer.Synthesize(ctx, delivery.Text)
	if synthErr != nil || len(audio) == 0 || len(audio) > mediaSizeLimit {
		return
	}
	if strings.TrimSpace(g.config.TTSUploadURL) != "" {
		// Third-party upload is skipped unless a future explicit upload worker is configured.
		return
	}
	_ = g.api.SendVoice(ctx, delivery.ChatID, audio, "")
}

func (g *Gateway) recordJob(job Job, outcome RunOutcome) {
	if g == nil || g.audit == nil {
		return
	}
	principal := job.PrincipalID
	if principal == "" {
		principal = job.UserID
	}
	if principal == "" {
		return
	}
	status := outcome.Status
	if status == "" {
		status = "completed"
	}
	event := audit.NewEvent("job", principal, status)
	event.JobID = job.ID
	event.HermesRunID = outcome.RunID
	event.RuntimeID = job.RuntimeID
	event.ContextID = job.ContextID
	event.PolicyRevision = job.PolicyVersion
	_ = g.audit.Append(event)
}

func (g *Gateway) user(id string) User {
	for _, user := range g.config.Users {
		if user.ID == id {
			return user
		}
	}
	return User{}
}

func (g *Gateway) userByChatID(chatID int64) User {
	for _, user := range g.config.Users {
		for _, telegramID := range user.TelegramIDs {
			if telegramID == chatID {
				return user
			}
		}
	}
	return User{}
}

// restartRequest builds the restart call for the runtime that actually ran
// the delivered job. Supervised gateways must address the supervisor with the
// job's durable identity so it can route to that user's spawned runtime;
// resident runtimes take an empty POST.
func (g *Gateway) restartRequest(delivery Delivery) (hubruntime.ExecuteRequest, bool) {
	if !g.config.Supervised {
		return hubruntime.ExecuteRequest{}, true
	}
	mapping, ok, err := g.spool.Mapping(delivery.JobID)
	if err != nil || !ok || mapping.PrincipalID == "" || mapping.ContextID == "" || mapping.RuntimeID == "" || mapping.PolicyVersion == "" || mapping.ExternalIdentityID == "" || mapping.ConversationID == "" || mapping.DeliveryTargetID == "" {
		return hubruntime.ExecuteRequest{}, false
	}
	return supervisorRestartRequest(
		identity.Envelope{Schema: identity.Schema, PrincipalID: mapping.PrincipalID, ExternalIdentityID: mapping.ExternalIdentityID, ContextID: mapping.ContextID, RuntimeID: mapping.RuntimeID, ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, PolicyVersion: mapping.PolicyVersion},
		mapping.OrganizationID, mapping.UserID, mapping.ActorID, mapping.ScopeID, delivery.JobID), true
}

// supervisorRestartRequest addresses the supervisor's restart route with the
// durable identity of the runtime that must be bounced.
func supervisorRestartRequest(env identity.Envelope, organizationID, userID, actorID, scopeID, idempotency string) hubruntime.ExecuteRequest {
	return hubruntime.ExecuteRequest{
		Envelope:       env,
		OrganizationID: organizationID,
		UserID:         userID,
		ActorID:        actorID,
		ScopeID:        scopeID,
		Channel:        "communication",
		Trigger:        "restart",
		JobID:          "restart-" + idempotency,
		IdempotencyKey: "restart-" + idempotency,
	}
}

func signalRestartAfterDelivery(stateDir string) error {
	restartPath := filepath.Join(stateDir, "restart.request")
	if _, err := os.Stat(restartPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(stateDir, "runtime.json"))
	if err != nil {
		return err
	}
	var status struct {
		PIDs []int `json:"pids"`
	}
	if err := json.Unmarshal(body, &status); err != nil || len(status.PIDs) == 0 || status.PIDs[0] <= 1 {
		return errors.New("invalid runtime marker")
	}
	process, err := os.FindProcess(status.PIDs[0])
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}

func connectionList(user User) string {
	lines := []string{"Подключения:"}
	for _, service := range stack.ServiceCatalog() {
		if contains(user.PolicyDisabled, service.Name) {
			lines = append(lines, service.Name+" — policy-disabled")
			continue
		}
		if !contains(user.Features, service.Name) {
			if service.SelfService {
				lines = append(lines, service.Name+" — доступен для включения; env: "+strings.Join(service.Requires, ", "))
			} else {
				lines = append(lines, service.Name+" — host-managed")
			}
			continue
		}
		missing := []string{}
		for _, key := range service.Requires {
			if !user.ConfiguredEnv[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) == 0 {
			lines = append(lines, service.Name+" — готово")
		} else {
			lines = append(lines, service.Name+" — нужны настройки: "+strings.Join(missing, ", "))
		}
	}
	return strings.Join(lines, "\n")
}

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func limitTelegramText(text string) string {
	runes := []rune(text)
	if len(runes) <= 4000 {
		return text
	}
	return string(runes[:4000])
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
