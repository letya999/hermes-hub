// Package media is the standard document and image profile Hermes reaches
// through the hub MCP server. Extract and create are separate effects.
// Image understanding and image generation use different credentials.
// Work runs only while a tool call is in flight.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

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

func (s *Session) extract(ctx context.Context, rel string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format, err := documentFormat(rel, true)
	if err != nil {
		return nil, err
	}
	body, err := s.readRegular(rel, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	text, err := documentText(format, body)
	if err != nil {
		return nil, err
	}
	pages := pageCount(text)
	if pages > MaxPages {
		return nil, fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	return map[string]any{
		"path": rel, "format": format, "text": text, "bytes": len(body),
		"pages": pages, "truncated": false, "secret_values_included": false,
	}, nil
}

func (s *Session) create(ctx context.Context, name, format, text string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if !slicesContains(DocumentFormats, format) {
		return nil, fmt.Errorf("unsupported document format %q", format)
	}
	if !validName(name) {
		return nil, errors.New("document name must be a short identifier")
	}
	body, plain, err := documentBody(format, text)
	if err != nil {
		return nil, err
	}
	rel := path.Join("artifacts", "documents", name+"."+format)
	if err := s.writeNew(rel, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": rel, "format": format, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func (s *Session) editDocument(ctx context.Context, rel, text string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rel, "artifacts/documents/") {
		return nil, errors.New("only generated documents can be edited")
	}
	format, err := documentFormat(rel, false)
	if err != nil {
		return nil, err
	}
	if _, err = s.readRegular(rel, MaxDocumentBytes); err != nil {
		return nil, err
	}
	body, plain, err := documentBody(format, text)
	if err != nil {
		return nil, err
	}
	if err = s.writeReplacing(rel, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": rel, "format": format, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func (s *Session) convertDocument(ctx context.Context, rel, name, format string) (map[string]any, error) {
	if err := s.docs.enter(); err != nil {
		return nil, err
	}
	defer s.docs.leave()
	ctx, cancel := context.WithTimeout(ctx, DocumentTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	from, err := documentFormat(rel, true)
	if err != nil {
		return nil, err
	}
	format = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(format), "."))
	if !validName(name) {
		return nil, errors.New("document name must be a short identifier")
	}
	if from == format {
		return nil, errors.New("document format is unchanged")
	}
	if !slicesContains(DocumentFormats, format) {
		return nil, fmt.Errorf("unsupported document format %q", format)
	}
	if format == "csv" && from != "xlsx" {
		return nil, fmt.Errorf("cannot convert %s to csv", from)
	}
	raw, err := s.readRegular(rel, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	plain, err := documentText(from, raw)
	if err != nil {
		return nil, err
	}
	body, plain, err := documentBody(format, plain)
	if err != nil {
		return nil, err
	}
	target := path.Join("artifacts", "documents", name+"."+format)
	if err = s.writeNew(target, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": target, "format": format, "source": rel, "bytes": len(body), "pages": pageCount(plain),
		"secret_values_included": false,
	}, nil
}

func documentText(format string, body []byte) (string, error) {
	switch format {
	case "html":
		if !utf8.Valid(body) {
			return "", errors.New("document must be UTF-8")
		}
		return htmlText(string(body)), nil
	case "pdf":
		return pdfText(body)
	case "xlsx":
		return xlsxText(body)
	case "pptx":
		return pptxText(body)
	default:
		if !utf8.Valid(body) {
			return "", errors.New("document must be UTF-8")
		}
		return string(body), nil
	}
}

func documentBody(format, text string) ([]byte, string, error) {
	if strings.Contains(text, "\x00") || !utf8.ValidString(text) {
		return nil, "", errors.New("document text must be UTF-8")
	}
	if len(text) > MaxDocumentBytes || containsSecret(text) {
		if containsSecret(text) {
			return nil, "", errors.New("refusing to store credential material")
		}
		return nil, "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	if pageCount(text) > MaxPages {
		return nil, "", fmt.Errorf("document exceeds %d pages", MaxPages)
	}
	var body []byte
	var err error
	switch format {
	case "html":
		body = []byte(htmlDocument(text))
	case "pdf":
		body, err = pdfBytes(text)
	case "xlsx":
		body, err = xlsxBytes(text)
	case "pptx":
		body, err = pptxBytes(text)
	default:
		body = []byte(text)
	}
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxDocumentBytes {
		return nil, "", fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	if containsSecretBytes(body) {
		return nil, "", errors.New("refusing to store credential material")
	}
	return body, text, nil
}

func htmlDocument(text string) string {
	return "<!DOCTYPE html>\n<meta charset=\"utf-8\">\n<article>\n" + htmlEscape(text) + "\n</article>\n"
}

func (s *Session) remove(rel string) (map[string]any, error) {
	if err := relPath(rel); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(rel, "artifacts/documents/") && !strings.HasPrefix(rel, "artifacts/images/") && !strings.HasPrefix(rel, "artifacts/videos/") {
		return nil, errors.New("only generated artifacts can be removed")
	}
	if path.Base(rel) == "." || strings.HasSuffix(rel, "/") {
		return nil, errors.New("regular file required")
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("regular file required")
	}
	if err := s.Workspace.Remove(rel); err != nil {
		return nil, err
	}
	return map[string]any{"path": rel, "removed": true, "secret_values_included": false}, nil
}

func documentFormat(rel string, extract bool) (string, error) {
	if err := relPath(rel); err != nil {
		return "", err
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(rel), "."))
	if slicesContains(UnsupportedDocuments, ext) || slicesContains(UnsupportedImages, ext) {
		return "", fmt.Errorf("unsupported document format %q", ext)
	}
	if ext == "htm" && extract {
		return "html", nil
	}
	if !slicesContains(DocumentFormats, ext) {
		return "", fmt.Errorf("unsupported document format %q", ext)
	}
	return ext, nil
}

func (s *Session) readRegular(rel string, limit int) ([]byte, error) {
	if s.Workspace == nil {
		return nil, errors.New("workspace is not configured")
	}
	if err := relPath(rel); err != nil {
		return nil, err
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("regular file required")
	}
	f, err := s.Workspace.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return body, nil
}

func (s *Session) writeNew(rel string, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWrite(rel, body); err != nil {
		return err
	}
	if _, err := s.Workspace.Lstat(rel); err == nil {
		return errors.New("artifact already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.commitNew(rel, body)
}

func (s *Session) writeReplacing(rel string, body []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareWrite(rel, body); err != nil {
		return err
	}
	info, err := s.Workspace.Lstat(rel)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("regular file required")
	}
	f, err := s.Workspace.OpenFile(rel, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *Session) prepareWrite(rel string, body []byte) error {
	if s.Workspace == nil {
		return errors.New("workspace is not configured")
	}
	if containsSecretBytes(body) {
		return errors.New("refusing to store credential material")
	}
	return s.Workspace.MkdirAll(path.Dir(rel), 0o700)
}

func (s *Session) commitNew(rel string, body []byte) error {
	tmp := path.Join(path.Dir(rel), ".hub-"+randomToken())
	if err := s.writeTemp(tmp, body); err != nil {
		return err
	}
	if err := s.Workspace.Rename(tmp, rel); err != nil {
		_ = s.Workspace.Remove(tmp)
		return err
	}
	return nil
}

func (s *Session) writeTemp(tmp string, body []byte) error {
	f, err := s.Workspace.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = s.Workspace.Remove(tmp)
		}
	}()
	if _, err = f.Write(body); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func relPath(p string) error {
	if p == "" || p == "." || !fs.ValidPath(p) || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || strings.ContainsRune(p, ':') || path.IsAbs(p) {
		return errors.New("relative path required")
	}
	return nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

func pageCount(text string) int {
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return 0
	}
	return (n + RunesPerPage - 1) / RunesPerPage
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "tmp"
	}
	return hex.EncodeToString(b[:])
}

func htmlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// htmlText drops well-formed script, style, noscript and comments.
// It is a text extract, not a browser.
func htmlText(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			next := strings.IndexByte(s[i:], '<')
			chunk := s[i:]
			if next >= 0 {
				chunk = s[i : i+next]
				i += next
			} else {
				i = len(s)
			}
			b.WriteString(decodeEntities(chunk))
			continue
		}
		if strings.HasPrefix(lower[i:], "<!--") {
			if j := strings.Index(lower[i+4:], "-->"); j >= 0 {
				i += 4 + j + 3
				continue
			}
			break
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		raw := strings.TrimSpace(lower[i+1 : i+end])
		raw = strings.TrimSuffix(raw, "/")
		closing := strings.HasPrefix(raw, "/")
		raw = strings.TrimPrefix(raw, "/")
		name := raw
		if field, _, _ := strings.Cut(raw, " "); field != "" {
			name = field
		}
		i += end + 1
		if !closing && (name == "script" || name == "style" || name == "noscript") {
			closer := "</" + name
			j := strings.Index(lower[i:], closer)
			if j < 0 {
				break
			}
			rest := i + j + len(closer)
			k := strings.IndexByte(s[rest:], '>')
			if k < 0 {
				break
			}
			i = rest + k + 1
			continue
		}
		if name == "br" || name == "p" || name == "div" || name == "li" || name == "tr" {
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(collapseSpace(b.String()))
}

func collapseSpace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == '\f' {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

func decodeEntities(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		semi := strings.IndexByte(s[i:], ';')
		if semi <= 1 || semi > 12 {
			b.WriteByte(s[i])
			i++
			continue
		}
		ent := s[i+1 : i+semi]
		i += semi + 1
		switch ent {
		case "amp":
			b.WriteByte('&')
		case "lt":
			b.WriteByte('<')
		case "gt":
			b.WriteByte('>')
		case "quot":
			b.WriteByte('"')
		case "apos":
			b.WriteByte('\'')
		case "nbsp":
			b.WriteByte(' ')
		default:
			if n, ok := numericEntity(ent); ok {
				b.WriteRune(n)
			} else {
				b.WriteByte('&')
				b.WriteString(ent)
				b.WriteByte(';')
			}
		}
	}
	return b.String()
}

func numericEntity(ent string) (rune, bool) {
	if len(ent) < 2 || ent[0] != '#' {
		return 0, false
	}
	n := 0
	if ent[1] == 'x' || ent[1] == 'X' {
		for _, r := range ent[2:] {
			n *= 16
			switch {
			case r >= '0' && r <= '9':
				n += int(r - '0')
			case r >= 'a' && r <= 'f':
				n += int(r-'a') + 10
			case r >= 'A' && r <= 'F':
				n += int(r-'A') + 10
			default:
				return 0, false
			}
		}
	} else {
		for _, r := range ent[1:] {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + int(r-'0')
		}
	}
	if n <= 0 || n > 0x10FFFF || n > 1<<21 {
		return 0, false
	}
	return rune(n), true
}

func knownSecrets() []string {
	var out []string
	for _, key := range []string{"OPENAI_API_KEY", "FAL_KEY"} {
		value := os.Getenv(key)
		if len(value) >= 8 {
			out = append(out, value)
		}
	}
	return out
}

func containsSecret(s string) bool {
	for _, secret := range knownSecrets() {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

func containsSecretBytes(b []byte) bool {
	for _, secret := range knownSecrets() {
		if bytes.Contains(b, []byte(secret)) {
			return true
		}
	}
	return false
}

func redact(s string) string {
	for _, secret := range knownSecrets() {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	return s
}

func redactErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redact(err.Error()))
}

func redactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	for key, value := range m {
		if text, ok := value.(string); ok {
			m[key] = redact(text)
		}
	}
	return m
}
