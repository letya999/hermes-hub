// Package mediasvc is the shared core of the hub's standalone media
// services: cmd/hub-stt serves transcription (sync + async jobs for long
// recordings), cmd/hub-tts serves synthesis, cmd/hub-media serves image and
// video generation — separate binaries, separate images, separate scaling.
// The HTTP surface is the OpenAI audio contract (transcriptions/speech) plus
// an async job API for long recordings and slow media generation.
package mediasvc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	brokeridentity "github.com/letya999/credential-broker/identity"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

// RouteHop is one candidate in a logical model's ordered fallback chain:
// the engine name and the provider-side model id it gets called with.
type RouteHop struct {
	Engine string
	Model  string
}

// Role selects which HTTP surface the process serves.
const (
	RoleSTT   = "stt"
	RoleTTS   = "tts"
	RoleMedia = "media"
)

type Config struct {
	Role       string
	ListenAddr string
	Auth       string   // required bearer token
	DataDir    string   // durable job store + scratch
	InputRoot  string   // local sources must resolve under this root
	FetchHosts []string // allowed https fetch hosts; empty disables remote fetch
	Workers    int      // concurrent engine calls
	Engine     string   // "remote" | "command" | "sherpa" | "fal" | "elevenlabs"
	// Engines lists the media engines to build in order (HUB_MEDIA_ENGINES);
	// empty means [Engine]. Routes maps a logical model id to an ordered
	// list of provider candidates (HUB_MEDIA_MODEL_ROUTES).
	Engines       []string
	Routes        map[string][]RouteHop
	Providers     map[string]MediaProvider
	Command       string // command engine argv0
	Upstream      string // remote engine base URL, e.g. http://speaches:8000
	UpstreamKey   string
	QueueUpstream string   // async queue base; fal default derives from Upstream
	ModelDir      string   // sherpa/native models
	STTModel      string   // model selector for stt
	TTSModel      string   // model selector for tts (sherpa)
	DiarizeModel  string   // pyannote segmentation model (sherpa)
	EmbedModel    string   // speaker embedding model (sherpa)
	Voice         string   // default TTS voice id
	Voices        []string // advertised TTS voice ids (HUB_TTS_VOICES)
	ImageModel    string   // media role: default image model
	ImageModels   []string // media role: allowed image models; empty = ImageModel only
	ChatModels    []string // media role: image models routed via /chat/completions (remote engine)
	VideoModel    string   // media role: default video model; empty = video disabled
	VideoModels   []string // media role: allowed video models; empty = VideoModel only
	// BrokerGrant names the Credential Broker grant that delivers the provider
	// key (HUB_MEDIA_BROKER_GRANT). Set: keys materialize per call through
	// broker leases and never enter env files. Empty: static env fallback.
	BrokerGrant   string
	Broker        credentialbroker.Config
	PrincipalID   string // identity envelope for the broker actor
	ContextID     string
	RuntimeID     string
	PolicyVersion string
	JobTTL        time.Duration // finished job retention
	JobTimeout    time.Duration // async job max runtime
	MaxUpload     int64         // sync upload cap
	MaxSource     int64         // async source/result cap
}

// MediaProvider separates each engine's endpoint and credential reference.
type MediaProvider struct {
	QueueUpstream string `json:"queue_upstream"`
	BrokerGrant   string `json:"broker_grant"`
	Upstream      string `json:"upstream"`
	KeyEnv        string `json:"key_env"`
}

// ConfigFromEnv keeps every knob in one place; the role is fixed by the
// binary (hub-stt vs hub-tts), not an env var — an empty value means the
// service is deliberately unconfigured rather than guessing.
func ConfigFromEnv(role string) (Config, error) {
	c := Config{
		Role:          role,
		ListenAddr:    envOr("HUB_MEDIA_LISTEN", "0.0.0.0:8090"),
		Auth:          os.Getenv("HUB_MEDIA_AUTH"),
		DataDir:       envOr("HUB_MEDIA_DATA", "/data/media"),
		InputRoot:     os.Getenv("HUB_MEDIA_INPUT_ROOT"),
		FetchHosts:    splitEnv("HUB_MEDIA_FETCH_HOSTS"),
		Workers:       envInt("HUB_MEDIA_WORKERS", 2),
		Engine:        envOr("HUB_MEDIA_ENGINE", "command"),
		Engines:       splitEnv("HUB_MEDIA_ENGINES"),
		Command:       os.Getenv("HUB_MEDIA_COMMAND"),
		Upstream:      normalizeUpstream(os.Getenv("HUB_MEDIA_UPSTREAM")),
		UpstreamKey:   os.Getenv("HUB_MEDIA_UPSTREAM_KEY"),
		ModelDir:      envOr("HUB_MEDIA_MODEL_DIR", "/data/models"),
		STTModel:      envOr("HUB_STT_MODEL", "whisper-tiny"),
		TTSModel:      envOr("HUB_TTS_MODEL", "vits-piper-ru_RU-dmitri"),
		DiarizeModel:  os.Getenv("HUB_DIARIZE_MODEL"),
		EmbedModel:    os.Getenv("HUB_EMBED_MODEL"),
		Voice:         envOr("HUB_TTS_VOICE", "xenia"),
		Voices:        splitEnv("HUB_TTS_VOICES"),
		ImageModel:    os.Getenv("HUB_MEDIA_IMAGE_MODEL"),
		ImageModels:   splitEnv("HUB_MEDIA_IMAGE_MODELS"),
		ChatModels:    splitEnv("HUB_MEDIA_CHAT_MODELS"),
		VideoModel:    os.Getenv("HUB_MEDIA_VIDEO_MODEL"),
		VideoModels:   splitEnv("HUB_MEDIA_VIDEO_MODELS"),
		QueueUpstream: normalizeUpstream(os.Getenv("HUB_MEDIA_QUEUE_UPSTREAM")),
		JobTTL:        envDur("HUB_MEDIA_JOB_TTL", 24*time.Hour),
		JobTimeout:    envDur("HUB_MEDIA_JOB_TIMEOUT", 30*time.Minute),
		MaxUpload:     64 << 20,
		MaxSource:     2 << 30,
		BrokerGrant:   strings.TrimSpace(os.Getenv("HUB_MEDIA_BROKER_GRANT")),
		PrincipalID:   envOr("HUB_PRINCIPAL_ID", os.Getenv("HUB_USER_ID")),
		ContextID:     os.Getenv("HUB_CONTEXT_ID"),
		RuntimeID:     envOr("HUB_RUNTIME_ID", ""),
		PolicyVersion: envOr("HUB_POLICY_VERSION", "policy-1"),
	}
	if c.ContextID == "" {
		c.ContextID = c.PrincipalID
	}
	if c.RuntimeID == "" {
		c.RuntimeID = c.PrincipalID
	}
	broker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_MEDIA_")
	if err != nil {
		return c, fmt.Errorf("media broker env: %w", err)
	}
	c.Broker = broker
	if c.Role != RoleSTT && c.Role != RoleTTS && c.Role != RoleMedia {
		return c, fmt.Errorf("role must be %q, %q or %q", RoleSTT, RoleTTS, RoleMedia)
	}
	if c.Auth == "" {
		return c, errors.New("HUB_MEDIA_AUTH is required")
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	if raw := os.Getenv("HUB_MEDIA_PROVIDERS"); raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c.Providers); err != nil {
			return c, errors.New("invalid HUB_MEDIA_PROVIDERS configuration")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return c, errors.New("invalid HUB_MEDIA_PROVIDERS trailing data")
		}
	}
	names := c.Engines
	if len(names) == 0 {
		names = []string{c.Engine}
	}
	known := map[string]bool{}
	for _, name := range names {
		if known[name] {
			return c, errors.New("duplicate media engine")
		}
		known[name] = true
	}
	for name, provider := range c.Providers {
		if provider.BrokerGrant != "" && (provider.KeyEnv != "" || !brokeridentity.ValidID(provider.BrokerGrant)) {
			return c, errors.New("media provider needs one valid credential reference")
		}
		if !known[name] {
			return c, errors.New("provider configuration names an unconfigured engine")
		}
		for _, endpoint := range []string{provider.Upstream, provider.QueueUpstream} {
			if endpoint == "" {
				continue
			}
			u, err := url.Parse(endpoint)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return c, errors.New("media provider needs a fixed upstream URL without credentials")
			}
		}
		if provider.KeyEnv != "" && !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(provider.KeyEnv) {
			return c, errors.New("media provider key_env is not an env name")
		}
	}
	switch c.Engine {
	case "comfy":
	case "remote":
		if c.Upstream == "" && c.Providers[c.Engine].Upstream == "" {
			return c, errors.New("HUB_MEDIA_UPSTREAM is required for the remote engine")
		}
	case "fal":
		if c.Role != RoleMedia {
			return c, errors.New("HUB_MEDIA_ENGINE=fal is a media engine")
		}
		if c.UpstreamKey == "" && os.Getenv("FAL_KEY") == "" && c.BrokerGrant == "" && c.Providers[c.Engine].KeyEnv == "" && c.Providers[c.Engine].BrokerGrant == "" {
			return c, errors.New("HUB_MEDIA_UPSTREAM_KEY, FAL_KEY or HUB_MEDIA_BROKER_GRANT is required for the fal engine")
		}
		if c.Upstream == "" {
			c.Upstream = "https://fal.run"
		}
	case "command", "command-serve", "sherpa":
		if c.Role == RoleMedia && c.Engine != "command" {
			return c, fmt.Errorf("HUB_MEDIA_ENGINE=%s is not a media engine", c.Engine)
		}
		if c.Engine == "command-serve" && c.Role == RoleTTS {
			return c, errors.New("HUB_MEDIA_ENGINE=command-serve is an STT engine")
		}
		if c.Engine == "command" || c.Engine == "command-serve" {
			if strings.TrimSpace(c.Command) == "" {
				return c, fmt.Errorf("HUB_MEDIA_COMMAND is required for the %s engine", c.Engine)
			}
		} else if c.ModelDir == "" {
			return c, errors.New("HUB_MEDIA_MODEL_DIR is required for the sherpa engine")
		}
	case "elevenlabs":
		if c.Role != RoleTTS {
			return c, errors.New("HUB_MEDIA_ENGINE=elevenlabs is a TTS engine")
		}
		if c.UpstreamKey == "" {
			return c, errors.New("HUB_MEDIA_UPSTREAM_KEY is required for the elevenlabs engine")
		}
	default:
		return c, fmt.Errorf("HUB_MEDIA_ENGINE must be remote, command, command-serve, sherpa, fal or elevenlabs, not %q", c.Engine)
	}
	if c.Role == RoleMedia {
		if len(c.Engines) == 0 {
			c.Engines = []string{c.Engine}
		}
		routes, err := parseRoutes(os.Getenv("HUB_MEDIA_MODEL_ROUTES"), c.Engines)
		if err != nil {
			return c, err
		}
		c.Routes = routes
		if c.ImageModel == "" {
			return c, errors.New("HUB_MEDIA_IMAGE_MODEL is required for the media role")
		}
		if len(c.ImageModels) == 0 {
			c.ImageModels = []string{c.ImageModel}
		}
		if c.VideoModel != "" && len(c.VideoModels) == 0 {
			c.VideoModels = []string{c.VideoModel}
		}
	}
	if c.InputRoot != "" {
		root, err := filepath.Abs(c.InputRoot)
		if err != nil {
			return c, err
		}
		c.InputRoot = root
	}
	return c, nil
}

// parseRoutes reads HUB_MEDIA_MODEL_ROUTES:
// "logical=engine:provider_id[,engine:provider_id];..." — every hop's engine
// must be one of the configured engines so a route can never name a
// provider the deployment did not bring up.
func parseRoutes(raw string, engines []string) (map[string][]RouteHop, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	known := toSet(engines)
	out := map[string][]RouteHop{}
	for _, entry := range strings.Split(raw, ";") {
		logical, hopsRaw, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || logical == "" {
			return nil, fmt.Errorf("HUB_MEDIA_MODEL_ROUTES: bad entry %q", entry)
		}
		if _, exists := out[logical]; exists {
			return nil, errors.New("duplicate media model route")
		}
		if len(strings.Split(hopsRaw, ",")) > 8 {
			return nil, errors.New("media routes allow at most eight hops")
		}
		var hops []RouteHop
		for _, hop := range strings.Split(hopsRaw, ",") {
			engine, model, ok := strings.Cut(strings.TrimSpace(hop), ":")
			if !ok || model == "" || !known[engine] || strings.Contains(model, "..") || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`).MatchString(model) {
				return nil, fmt.Errorf("HUB_MEDIA_MODEL_ROUTES: bad hop %q in %q", hop, logical)
			}
			hops = append(hops, RouteHop{Engine: engine, Model: model})
		}
		out[logical] = hops
	}
	return out, nil
}

// normalizeUpstream accepts both bare hosts (http://speaches:8000) and full
// API bases (https://api.groq.com/openai/v1); endpoints append the
// OpenAI-style path themselves.
func normalizeUpstream(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func splitEnv(key string) []string {
	var out []string
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if h := strings.ToLower(strings.TrimSpace(part)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func envInt(key string, fallback int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && n > 0 {
		return n
	}
	return fallback
}

func envDur(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key))); err == nil && d > 0 {
		return d
	}
	return fallback
}
