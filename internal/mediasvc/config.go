// Package mediasvc is the shared core of the hub's two standalone speech
// services: cmd/hub-stt serves transcription (sync + async jobs for long
// recordings), cmd/hub-tts serves synthesis — separate binaries, separate
// images, separate scaling. The HTTP surface is the OpenAI audio contract
// (transcriptions/speech) plus an async job API for long recordings fetched
// from user storage, presigned URLs or uploads.
package mediasvc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Role selects which HTTP surface the process serves.
const (
	RoleSTT = "stt"
	RoleTTS = "tts"
)

type Config struct {
	Role         string
	ListenAddr   string
	Auth         string   // required bearer token
	DataDir      string   // durable job store + scratch
	InputRoot    string   // local sources must resolve under this root
	FetchHosts   []string // allowed https fetch hosts; empty disables remote fetch
	Workers      int      // concurrent engine calls
	Engine       string   // "remote" | "command" | "sherpa"
	Command      string   // command engine argv0
	Upstream     string   // remote engine base URL, e.g. http://speaches:8000
	UpstreamKey  string
	ModelDir     string        // sherpa/native models
	STTModel     string        // model selector for stt
	TTSModel     string        // model selector for tts (sherpa)
	DiarizeModel string        // pyannote segmentation model (sherpa)
	EmbedModel   string        // speaker embedding model (sherpa)
	Voice        string        // default TTS voice id
	Voices       []string      // advertised TTS voice ids (HUB_TTS_VOICES)
	JobTTL       time.Duration // finished job retention
	MaxUpload    int64         // sync upload cap
	MaxSource    int64         // async source cap
}

// ConfigFromEnv keeps every knob in one place; the role is fixed by the
// binary (hub-stt vs hub-tts), not an env var — an empty value means the
// service is deliberately unconfigured rather than guessing.
func ConfigFromEnv(role string) (Config, error) {
	c := Config{
		Role:         role,
		ListenAddr:   envOr("HUB_MEDIA_LISTEN", "0.0.0.0:8090"),
		Auth:         os.Getenv("HUB_MEDIA_AUTH"),
		DataDir:      envOr("HUB_MEDIA_DATA", "/data/media"),
		InputRoot:    os.Getenv("HUB_MEDIA_INPUT_ROOT"),
		FetchHosts:   splitEnv("HUB_MEDIA_FETCH_HOSTS"),
		Workers:      envInt("HUB_MEDIA_WORKERS", 2),
		Engine:       envOr("HUB_MEDIA_ENGINE", "command"),
		Command:      os.Getenv("HUB_MEDIA_COMMAND"),
		Upstream:     normalizeUpstream(os.Getenv("HUB_MEDIA_UPSTREAM")),
		UpstreamKey:  os.Getenv("HUB_MEDIA_UPSTREAM_KEY"),
		ModelDir:     envOr("HUB_MEDIA_MODEL_DIR", "/data/models"),
		STTModel:     envOr("HUB_STT_MODEL", "whisper-tiny"),
		TTSModel:     envOr("HUB_TTS_MODEL", "vits-piper-ru_RU-dmitri"),
		DiarizeModel: os.Getenv("HUB_DIARIZE_MODEL"),
		EmbedModel:   os.Getenv("HUB_EMBED_MODEL"),
		Voice:        envOr("HUB_TTS_VOICE", "xenia"),
		Voices:       splitEnv("HUB_TTS_VOICES"),
		JobTTL:       envDur("HUB_MEDIA_JOB_TTL", 24*time.Hour),
		MaxUpload:    64 << 20,
		MaxSource:    2 << 30,
	}
	if c.Role != RoleSTT && c.Role != RoleTTS {
		return c, fmt.Errorf("role must be %q or %q", RoleSTT, RoleTTS)
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
	case "command":
		if strings.TrimSpace(c.Command) == "" {
			return c, errors.New("HUB_MEDIA_COMMAND is required for the command engine")
		}
	case "sherpa":
		if c.ModelDir == "" {
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
		return c, fmt.Errorf("HUB_MEDIA_ENGINE must be remote, command, sherpa or elevenlabs, not %q", c.Engine)
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
