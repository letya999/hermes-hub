// Package contract describes reviewed MCP credential requirements, independently
// of secret ownership, provider storage and process execution scope.
package contract

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/letya999/credential-broker/identity"
	"github.com/letya999/credential-broker/internal/strictjson"
)

const MaxBundle = 256 << 10

var ErrContract = errors.New("unsupported or invalid credential contract")
var ErrValues = errors.New("credential fields do not match contract")
var fieldID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,79}$`)
var stateName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,79}$`)
var i18nTag = regexp.MustCompile(`^[a-z]{2,8}(-[A-Za-z0-9]{1,8}){0,3}$`)

// validI18n bounds a translation map: a handful of plain-language tags and
// values staying within twice the label/title limit — translations of a
// maximum-length label run longer in non-Latin scripts.
func validI18n(table map[string]string) bool {
	if len(table) > 8 {
		return false
	}
	for tag, text := range table {
		if !i18nTag.MatchString(tag) || len(text) == 0 || len(text) > 240 {
			return false
		}
	}
	return true
}

type Field struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// LabelI18n carries deployer-declared translations of Label keyed by a
	// language tag (en, ru, kk, es, es-ES, ...). The browser form resolves
	// the viewer's language and falls back to Label when no entry matches.
	LabelI18n map[string]string `json:"label_i18n,omitempty"`
	Kind      string            `json:"kind"` // secret, string, json, pem, blob
	Required  bool              `json:"required"`
	MaxBytes  int               `json:"max_bytes"`
}
type Delivery struct {
	Type    string            `json:"type"` // env, file, json_file
	Field   string            `json:"field,omitempty"`
	Target  string            `json:"target"`
	EnvName string            `json:"env_name,omitempty"` // a FILE PATH, never its contents
	Mapping map[string]string `json:"mapping,omitempty"`  // JSON keys -> input field
}
type StateFile struct {
	Name     string `json:"name"`
	MaxBytes int    `json:"max_bytes"`
	JSON     bool   `json:"json"`
}
type State struct {
	Target string      `json:"target"`
	Files  []StateFile `json:"files"`
}
type Route struct {
	ID            string            `json:"id"`
	BaseURL       string            `json:"base_url"`
	PathPrefixes  []string          `json:"path_prefixes"`
	Methods       []string          `json:"methods"`
	Auth          string            `json:"auth"` // bearer, header, basic, oauth2, mtls
	Field         string            `json:"field,omitempty"`
	PasswordField string            `json:"password_field,omitempty"`
	Header        string            `json:"header,omitempty"`
	StaticHeaders map[string]string `json:"static_headers,omitempty"`
}
type Contract struct {
	ID            string     `json:"id"`
	Revision      int        `json:"revision"`
	Title         string     `json:"title"`
	Storage       string     `json:"storage"` // configured writable provider
	Fields        []Field    `json:"fields"`
	Deliveries    []Delivery `json:"deliveries,omitempty"`
	State         *State     `json:"state,omitempty"`
	Routes        []Route    `json:"routes,omitempty"`
	OAuthProvider string     `json:"oauth_provider,omitempty"`
	// Intake names a non-form collection path the channel can offer for this
	// contract (e.g. telegram_qr — QR login minted by the hub instead of
	// pasting a session into the Broker form). Unknown values are rejected so
	// a new intake kind is always a deliberate contract revision.
	Intake      string `json:"intake,omitempty"`
	AllowShared bool   `json:"allow_shared"`
	// TitleI18n mirrors Field.LabelI18n at contract level: per-language form
	// titles. Deployer-declared, resolved per request, Title is the fallback.
	TitleI18n map[string]string `json:"title_i18n,omitempty"`
}

// Localize resolves table[lang] for lang or its base tag (es-ES → es); an
// empty table or a miss keeps the base text. Deployer-declared i18n never
// overrides the reviewed fallback.
func Localize(table map[string]string, lang, base string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if text := table[lang]; text != "" {
		return text
	}
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		if text := table[lang[:i]]; text != "" {
			return text
		}
	}
	return base
}

// TitleFor renders the contract title in lang with the reviewed Title as
// fallback.
func (c Contract) TitleFor(lang string) string {
	return Localize(c.TitleI18n, lang, c.Title)
}

// LabelFor renders the field label in lang with the reviewed Label as
// fallback.
func (f Field) LabelFor(lang string) string {
	return Localize(f.LabelI18n, lang, f.Label)
}

// MaxExtraInputs bounds one request's optional extras. Upstream metadata can
// declare dozens of settings (a gitlab-class connector lists ~50), so the
// catalog contract's 32-field ceiling does not apply — extras extend one form,
// not the audited catalog surface, but still need a hard cap.
const MaxExtraInputs = 64

// Extra attaches request-scoped optional env inputs to one credential request.
// Reviewed contracts stay immutable; extras extend only the credential minted
// from that request — they are validated by the same rules as contract fields
// but can never be required or change where existing fields go.
type Extra struct {
	Fields     []Field    `json:"fields,omitempty"`
	Deliveries []Delivery `json:"deliveries,omitempty"`
}

// Validate checks extras against the same field and env-delivery rules as a
// contract member, plus the extras-only invariants: never required, env-only,
// every field delivered exactly once.
func (e *Extra) Validate() error {
	if e == nil {
		return nil
	}
	if len(e.Fields) > MaxExtraInputs || len(e.Deliveries) > MaxExtraInputs {
		return ErrContract
	}
	fields := map[string]bool{}
	for _, f := range e.Fields {
		if f.Required || !fieldID.MatchString(f.ID) || f.ID == "csrf" || fields[f.ID] || len(f.Label) == 0 || len(f.Label) > 120 || f.MaxBytes < 1 || f.MaxBytes > 65536 || !slices.Contains([]string{"secret", "string", "json", "pem", "blob"}, f.Kind) || !validI18n(f.LabelI18n) {
			return ErrContract
		}
		fields[f.ID] = true
	}
	targets := map[string]bool{}
	for _, d := range e.Deliveries {
		if d.Type != "env" || !envName.MatchString(d.Target) || targets[d.Target] || !fields[d.Field] || d.EnvName != "" || len(d.Mapping) != 0 {
			return ErrContract
		}
		targets[d.Target] = true
		delete(fields, d.Field)
	}
	if len(fields) != 0 {
		return ErrContract
	}
	return nil
}

func SafeTarget(s string) bool {
	if !strings.HasPrefix(s, "/") || path.Clean(s) != s || s == "/" || strings.ContainsAny(s, "\\\x00\r\n") {
		return false
	}
	for _, p := range []string{"/proc", "/sys", "/dev", "/etc", "/bin", "/sbin", "/usr", "/var/run"} {
		if s == p || strings.HasPrefix(s, p+"/") {
			return false
		}
	}
	return len(s) <= 240
}
func SafeRelative(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\x00\r\n%?#") || path.Clean(p) != p {
		return false
	}
	return len(p) <= 2048
}
func HeaderName(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func forbiddenHeader(h string) bool {
	switch strings.ToLower(h) {
	case "host", "connection", "proxy-authorization", "proxy-authenticate", "cookie", "set-cookie", "transfer-encoding", "content-length", "upgrade", "trailer", "te":
		return true
	}
	return strings.HasPrefix(strings.ToLower(h), "x-forwarded-")
}
func (c Contract) Validate() error {
	if len(c.Fields) > 32 || len(c.Deliveries) > 32 || len(c.Routes) > 8 {
		return ErrContract
	}
	return c.ValidateStructure()
}

// ValidateStructure applies every per-field, delivery, state and route rule
// except the catalog cardinality caps — the effective contract one request
// collects may exceed them through validated optional extras.
func (c Contract) ValidateStructure() error {
	if !identity.ValidID(c.ID) || c.Revision < 1 || c.Revision > 100000 || len(c.Title) == 0 || len(c.Title) > 120 || !identity.ValidID(c.Storage) || !validI18n(c.TitleI18n) {
		return ErrContract
	}
	fields := map[string]bool{}
	for _, f := range c.Fields {
		if !fieldID.MatchString(f.ID) || f.ID == "csrf" || fields[f.ID] || len(f.Label) == 0 || len(f.Label) > 120 || f.MaxBytes < 1 || f.MaxBytes > 65536 || !slices.Contains([]string{"secret", "string", "json", "pem", "blob"}, f.Kind) || !validI18n(f.LabelI18n) {
			return ErrContract
		}
		fields[f.ID] = true
	}
	if c.OAuthProvider != "" && !identity.ValidID(c.OAuthProvider) {
		return ErrContract
	}
	// Intake values are a reviewed, closed vocabulary — a new collection path
	// is a deliberate contract revision, not a free-form annotation.
	if c.Intake != "" && c.Intake != "telegram_qr" {
		return ErrContract
	}
	if len(c.Fields) == 0 && c.OAuthProvider == "" {
		return ErrContract
	}
	targets := map[string]bool{}
	for _, d := range c.Deliveries {
		if targets[d.Target] {
			return ErrContract
		}
		targets[d.Target] = true
		switch d.Type {
		case "env":
			if !envName.MatchString(d.Target) || !fields[d.Field] || d.EnvName != "" || len(d.Mapping) != 0 {
				return ErrContract
			}
		case "file":
			if !SafeTarget(d.Target) || !fields[d.Field] || len(d.Mapping) != 0 {
				return ErrContract
			}
		case "json_file":
			if !SafeTarget(d.Target) || d.Field != "" || len(d.Mapping) == 0 || len(d.Mapping) > 32 {
				return ErrContract
			}
			for k, v := range d.Mapping {
				if len(k) == 0 || len(k) > 80 || !fields[v] {
					return ErrContract
				}
			}
		default:
			return ErrContract
		}
		if d.EnvName != "" && (!envName.MatchString(d.EnvName) || targets[d.EnvName]) {
			return ErrContract
		}
		if d.EnvName != "" {
			targets[d.EnvName] = true
		}
	}
	if c.State != nil {
		if !SafeTarget(c.State.Target) || len(c.State.Files) == 0 || len(c.State.Files) > 16 || c.AllowShared {
			return ErrContract
		}
		names := map[string]bool{}
		for _, f := range c.State.Files {
			if !stateName.MatchString(f.Name) || strings.Contains(f.Name, "..") || names[f.Name] || f.MaxBytes < 1 || f.MaxBytes > 65536 {
				return ErrContract
			}
			names[f.Name] = true
		}
		for t := range targets {
			if t == c.State.Target || strings.HasPrefix(t, c.State.Target+"/") {
				return ErrContract
			}
		}
	}
	routeIDs := map[string]bool{}
	for _, r := range c.Routes {
		u, e := url.Parse(r.BaseURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.HasSuffix(u.Hostname(), ".") || routeIDs[r.ID] || !identity.ValidID(r.ID) {
			return ErrContract
		}
		routeIDs[r.ID] = true
		if len(r.PathPrefixes) == 0 || len(r.PathPrefixes) > 16 || len(r.Methods) == 0 {
			return ErrContract
		}
		for _, p := range r.PathPrefixes {
			if !SafeRelative(p) {
				return ErrContract
			}
		}
		for _, m := range r.Methods {
			if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}, m) {
				return ErrContract
			}
		}
		switch r.Auth {
		case "bearer":
			if !fields[r.Field] {
				return ErrContract
			}
		case "header":
			if !fields[r.Field] || !HeaderName(r.Header) || forbiddenHeader(r.Header) {
				return ErrContract
			}
		case "basic", "mtls":
			if !fields[r.Field] || !fields[r.PasswordField] {
				return ErrContract
			}
		case "oauth2":
			if c.OAuthProvider == "" {
				return ErrContract
			}
		default:
			return ErrContract
		}
		for k, v := range r.StaticHeaders {
			if !HeaderName(k) || forbiddenHeader(k) || strings.EqualFold(k, "Authorization") || strings.EqualFold(k, r.Header) || strings.ContainsAny(v, "\r\n\x00") || len(v) > 1024 {
				return ErrContract
			}
		}
	}
	return nil
}
func (c Contract) Digest() string {
	b, _ := json.Marshal(c)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c Contract) ValidateValues(values map[string][]byte) error {
	if len(values) > len(c.Fields) {
		return ErrValues
	}
	total := 0
	known := map[string]bool{}
	for _, f := range c.Fields {
		v, ok := values[f.ID]
		known[f.ID] = true
		total += len(v)
		if !ok || len(v) == 0 {
			if f.Required {
				return ErrValues
			}
			continue
		}
		if len(v) > f.MaxBytes {
			return ErrValues
		}
		switch f.Kind {
		case "secret", "string":
			if !utf8.Valid(v) || strings.ContainsAny(string(v), "\x00\r\n") {
				return ErrValues
			}
		case "json":
			if !strictjson.Valid(v) || strings.TrimSpace(string(v)) == "null" {
				return ErrValues
			}
		case "pem":
			b, rest := pem.Decode(v)
			if b == nil || len(strings.TrimSpace(string(rest))) != 0 || len(b.Headers) > 0 {
				return ErrValues
			}
			switch b.Type {
			case "PRIVATE KEY":
				if _, err := x509.ParsePKCS8PrivateKey(b.Bytes); err != nil {
					return ErrValues
				}
			case "RSA PRIVATE KEY":
				if _, err := x509.ParsePKCS1PrivateKey(b.Bytes); err != nil {
					return ErrValues
				}
			case "EC PRIVATE KEY":
				if _, err := x509.ParseECPrivateKey(b.Bytes); err != nil {
					return ErrValues
				}
			case "CERTIFICATE":
				if _, err := x509.ParseCertificate(b.Bytes); err != nil {
					return ErrValues
				}
			default:
				return ErrValues
			}
		}
	}
	for k := range values {
		if !known[k] {
			return ErrValues
		}
	}
	if total > MaxBundle {
		return ErrValues
	}
	// Process environment cannot contain NUL even when the underlying field is a blob.
	for _, d := range c.Deliveries {
		if d.Type == "env" && strings.ContainsRune(string(values[d.Field]), 0) {
			return ErrValues
		}
	}
	return nil
}
func (r Route) Allows(method, p string) bool {
	if !slices.Contains(r.Methods, method) || !SafeRelative(p) {
		return false
	}
	for _, prefix := range r.PathPrefixes {
		if prefix == "/" || p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
func (r Route) HeaderName() string {
	if r.Auth == "header" {
		return http.CanonicalHeaderKey(r.Header)
	}
	return "Authorization"
}

// Compatible permits different delivery layouts only when the input schema and
// OAuth provider are identical. ToolHub must still approve the destination grant.
func Compatible(a, b Contract) bool {
	if a.OAuthProvider != b.OAuthProvider || len(a.Fields) != len(b.Fields) {
		return false
	}
	fa := append([]Field(nil), a.Fields...)
	fb := append([]Field(nil), b.Fields...)
	slices.SortFunc(fa, func(x, y Field) int { return strings.Compare(x.ID, y.ID) })
	slices.SortFunc(fb, func(x, y Field) int { return strings.Compare(x.ID, y.ID) })
	for i := range fa {
		if fa[i].ID != fb[i].ID || fa[i].Kind != fb[i].Kind || fa[i].Required != fb[i].Required || fa[i].MaxBytes != fb[i].MaxBytes {
			return false
		}
	}
	return true
}
