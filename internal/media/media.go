package media

import (
	"bytes"
	"context"
	"errors"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Package media is the standard document and image profile Hermes reaches
// through the hub MCP server. Extract and create are separate effects.
// Image understanding and image generation use different credentials.
// Work runs only while a tool call is in flight.

const (
	// HermesCommit is the pinned nousresearch/hermes-agent revision.
	// That tree has no document toolset. Its local vision backend reads any
	// container path, and image_generate returns a remote URL.
	HermesCommit  = "869228cab4a8276d3b4c78da9d9939670c47bd0f"
	HermesVersion = "0.21.0"

	// ImageModel is DEFAULT_MODEL in tools/image_generation_catalog.py at HermesCommit.
	ImageModel   = "fal-ai/flux-2/klein/9b"
	FalProvider  = "fal"
	FalImageSize = "square_hd"
	FalSteps     = 4
	FalFormat    = "png"

	DocumentConcurrency = 2
	InspectConcurrency  = 2
	GenerateConcurrency = 1

	DocumentTimeout = 5 * time.Second
	InspectTimeout  = 30 * time.Second
	GenerateTimeout = 60 * time.Second

	MaxDocumentBytes = 2 << 20
	RunesPerPage     = 3000
	MaxPages         = 20
	MaxImageBytes    = 5 << 20
	MaxPixels        = 4_000_000
	MaxEdge          = 4096
	MaxPromptRunes   = 2000

	configLimit = 256 << 10
)

// DocumentFormats are the extract and create formats. htm is accepted only on extract.
// pdf, xlsx, and pptx are simple text packages, not a layout office suite.
var DocumentFormats = []string{"txt", "md", "csv", "html", "pdf", "xlsx", "pptx"}

// UnsupportedDocuments are types this profile does not claim.
var UnsupportedDocuments = []string{"doc", "docx", "xls", "ppt", "odt", "rtf", "epub", "pages"}

// ImageFormats are the inspect and convert formats. Generation writes png or the bytes the provider returned.
var ImageFormats = []string{"png", "jpeg", "webp"}

// UnsupportedImages are formats this profile does not decode.
var UnsupportedImages = []string{"gif", "bmp", "svg", "tif", "tiff", "heic", "avif"}

// Session is one user's workspace. It is not a long-running service.
type Session struct {
	Workspace *os.Root
	HTTP      *http.Client
	FalBase   string

	docs    gate
	see     gate
	gen     gate
	writeMu sync.Mutex
}

type gate struct {
	mu  sync.Mutex
	in  int
	max int
}

func (g *gate) enter() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.max < 1 || g.in >= g.max {
		return errors.New("concurrency limit reached")
	}
	g.in++
	return nil
}

func (g *gate) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.in > 0 {
		g.in--
	}
}

// New binds the profile to one workspace root.
func New(root *os.Root) *Session {
	return &Session{
		Workspace: root,
		HTTP:      &http.Client{CheckRedirect: refuseRedirect},
		FalBase:   "https://fal.run",
		docs:      gate{max: DocumentConcurrency},
		see:       gate{max: InspectConcurrency},
		gen:       gate{max: GenerateConcurrency},
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("redirect refused")
}

type hermesConfig struct {
	Model struct {
		Default string `yaml:"default"`
		BaseURL string `yaml:"base_url"`
	} `yaml:"model"`
	ImageGen ImageGen `yaml:"image_gen"`
}

func (s *Session) configPath() string {
	if p := strings.TrimSpace(os.Getenv("HUB_HERMES_CONFIG")); p != "" {
		return p
	}
	return "/state/hermes/config.yaml"
}

func (s *Session) readConfig() (hermesConfig, error) {
	var cfg hermesConfig
	body, err := os.ReadFile(s.configPath()) // #nosec G304 -- path is the mounted Hermes config or an operator override
	if err != nil {
		return cfg, errors.New("media profile config is unavailable")
	}
	if len(body) > configLimit {
		return cfg, errors.New("media profile config is too large")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return cfg, errors.New("media profile config is invalid")
	}
	if err := doc.Decode(&cfg); err != nil {
		return cfg, errors.New("media profile config is invalid")
	}
	gen, err := strictImageGen(&doc)
	if err != nil {
		return cfg, err
	}
	cfg.ImageGen = gen
	return cfg, nil
}

// strictImageGen decodes only the image_gen mapping and rejects unknown fields.
// The rest of the Hermes file is upstream config this profile does not own.
func strictImageGen(doc *yaml.Node) (ImageGen, error) {
	node := doc
	if node != nil && node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return ImageGen{}, nil
		}
		node = node.Content[0]
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return ImageGen{}, errors.New("media profile config is invalid")
	}
	var imageNode *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "image_gen" {
			imageNode = node.Content[i+1]
		}
	}
	if imageNode == nil {
		return ImageGen{}, nil
	}
	raw, err := yaml.Marshal(imageNode)
	if err != nil {
		return ImageGen{}, errors.New("media profile config is invalid")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var gen ImageGen
	if err := dec.Decode(&gen); err != nil {
		return ImageGen{}, errors.New("media profile config is invalid")
	}
	return gen, nil
}

// ImageGranted reports whether the mounted Hermes config names a known image
// provider. A revoked feature disappears from that file on the next render.
// An unknown provider, model, delivery, or field fails closed.
func (s *Session) ImageGranted() bool {
	_, _, err := s.grantedImage()
	return err == nil
}

func (s *Session) grantedImage() (ImageGen, hermesConfig, error) {
	cfg, err := s.readConfig()
	if err != nil {
		return ImageGen{}, cfg, err
	}
	if strings.TrimSpace(cfg.ImageGen.Provider) == "" {
		return ImageGen{}, cfg, errors.New("image generation grant is not active")
	}
	gen, err := cfg.ImageGen.Normalize()
	if err != nil {
		return ImageGen{}, cfg, err
	}
	return gen, cfg, nil
}

func (s *Session) Extract(ctx context.Context, rel string) (map[string]any, error) {
	out, err := s.extract(ctx, rel)
	return redactMap(out), redactErr(err)
}

func (s *Session) Create(ctx context.Context, name, format, text string) (map[string]any, error) {
	out, err := s.create(ctx, name, format, text)
	return redactMap(out), redactErr(err)
}

// EditDocument replaces the text of one file under artifacts/documents.
func (s *Session) EditDocument(ctx context.Context, rel, text string) (map[string]any, error) {
	out, err := s.editDocument(ctx, rel, text)
	return redactMap(out), redactErr(err)
}

// ConvertDocument writes a new artifact in another supported format.
func (s *Session) ConvertDocument(ctx context.Context, rel, name, format string) (map[string]any, error) {
	out, err := s.convertDocument(ctx, rel, name, format)
	return redactMap(out), redactErr(err)
}

func (s *Session) Remove(rel string) (map[string]any, error) {
	out, err := s.remove(rel)
	return redactMap(out), redactErr(err)
}
