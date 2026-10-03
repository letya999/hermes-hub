// Package mediasvc is the shared core of the hub's standalone media
// services: cmd/hub-stt serves transcription (sync + async jobs for long
// recordings), cmd/hub-tts serves synthesis, cmd/hub-media serves image and
// video generation — separate binaries, separate images, separate scaling.
// The HTTP surface is the OpenAI audio contract (transcriptions/speech) plus
// an async job API for long recordings and slow media generation.
package mediasvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

// Role selects which HTTP surface the process serves.
const (
	RoleSTT   = "stt"
	RoleTTS   = "tts"
	RoleMedia = "media"
)

type Config struct {
	Role          string
	ListenAddr    string
	Auth          string   // required bearer token
	DataDir       string   // durable job store + scratch
	InputRoot     string   // local sources must resolve under this root
	FetchHosts    []string // allowed https fetch hosts; empty disables remote fetch
	Workers       int      // concurrent engine calls
	Engine        string   // "remote" | "command" | "sherpa" | "fal" | "elevenlabs"
	Command       string   // command engine argv0
	Upstream      string   // remote engine base URL, e.g. http://speaches:8000
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
	switch c.Engine {
	case "remote":
		if c.Upstream == "" {
			return c, errors.New("HUB_MEDIA_UPSTREAM is required for the remote engine")
		}
	case "fal":
		if c.Role != RoleMedia {
			return c, errors.New("HUB_MEDIA_ENGINE=fal is a media engine")
		}
		if c.UpstreamKey == "" && os.Getenv("FAL_KEY") == "" && c.BrokerGrant == "" {
			return c, errors.New("HUB_MEDIA_UPSTREAM_KEY, FAL_KEY or HUB_MEDIA_BROKER_GRANT is required for the fal engine")
		}
		if c.Upstream == "" {
			c.Upstream = "https://fal.run"
		}
	case "command", "sherpa":
		if c.Role == RoleMedia {
			return c, fmt.Errorf("HUB_MEDIA_ENGINE=%s is not a media engine", c.Engine)
		}
		if c.Engine == "command" {
			if strings.TrimSpace(c.Command) == "" {
				return c, errors.New("HUB_MEDIA_COMMAND is required for the command engine")
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
		return c, fmt.Errorf("HUB_MEDIA_ENGINE must be remote, command, sherpa, fal or elevenlabs, not %q", c.Engine)
	}
	if c.Role == RoleMedia {
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
