// Package hindsight is the first external memory.Provider adapter
// (SPEC-0085 R11, ADR-0086 D15): it speaks the Hindsight HTTP API verified
// at upstream pin 869228cab4a8276d3b4c78da9d9939670c47bd0f — never the
// upstream plugin, which would bypass hub-owned scope, provenance and
// governed-write invariants.
//
// Wire mapping (standard library only):
//
//	Capture -> POST /v1/default/banks/{bank}/memories (document_id upsert)
//	Export  -> GET  /v1/default/banks/{bank}/documents (+ per-doc detail)
//
// bank_id is the hub-minted opaque scope key verbatim; hub-owned fields ride
// item metadata and return through document_metadata.
package hindsight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Provider is a memory.Provider over the Hindsight HTTP API.
type Provider struct {
	base  string
	token string // optional bearer for explicitly granted cloud egress
	hc    *http.Client
}

// Option configures the adapter.
type Option func(*Provider)

// WithToken sets a bearer credential resolved by the hub from secrets —
// never stored in rendered config or diagnostics.
func WithToken(token string) Option {
	return func(p *Provider) { p.token = token }
}

// New binds an adapter to a Hindsight base URL (e.g.
// http://open-knowledge:8888 or a granted cloud endpoint).
func New(base string, opts ...Option) (*Provider, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("hindsight: invalid endpoint %q", base)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("hindsight: endpoint scheme %q is not http/https", u.Scheme)
	}
	p := &Provider{base: strings.TrimRight(base, "/"), hc: &http.Client{Timeout: 30 * time.Second}}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Caps advertises the lanes this backend honestly serves: capture, query
// search and export. The pre-turn recall block and governed file writes
// have no Hindsight semantic and stay absent — empty caps, never faked.
func (p *Provider) Caps(context.Context) memory.Caps {
	return memory.Caps{Capture: true, Search: true, Export: true, Vector: true}
}

// retainItem is the verified retain request item shape (document_id gives
// upsert semantics — a repeated capture replaces, never duplicates).
type retainItem struct {
	Content    string            `json:"content"`
	Context    string            `json:"context,omitempty"`
	DocumentID string            `json:"document_id,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// Metadata keys carry hub-owned fields through document_metadata untouched.
const (
	mdProvenance = "hub_provenance"
	mdKind       = "hub_kind"
	mdAudience   = "hub_audience"
	mdSourceRef  = "hub_source_ref"
	mdEntryID    = "hub_entry_id"
)

// Capture retains one entry as a document under the opaque scope bank. The
// entry id becomes document_id so provider-side upsert makes capture
// idempotent; provenance fields ride metadata verbatim — the provider never
// mints or rewrites them.
func (p *Provider) Capture(ctx context.Context, e memory.Entry) (string, error) {
	if err := checkScopeKey(e.Scope.Key); err != nil {
		return "", err
	}
	if strings.TrimSpace(e.ID) == "" {
		return "", fmt.Errorf("hindsight: entry id required for document upsert")
	}
	md := map[string]string{
		mdProvenance: string(e.Provenance),
		mdKind:       string(e.Kind),
		mdAudience:   string(e.Audience),
		mdEntryID:    e.ID,
	}
	if e.SourceRef != "" {
		md[mdSourceRef] = e.SourceRef
	}
	body := map[string]any{
		"async": false,
		"items": []retainItem{{
			Content:    e.Body,
			DocumentID: e.ID,
			Timestamp:  e.ObservedAt.UTC().Format(time.RFC3339),
			Metadata:   md,
		}},
	}
	var resp struct {
		Success bool `json:"success"`
	}
	if err := p.do(ctx, http.MethodPost, "/v1/default/banks/"+url.PathEscape(e.Scope.Key)+"/memories", body, &resp); err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("hindsight: retain rejected (success=false)")
	}
	return e.ID, nil
}

// Export materializes the bank back into entries: the documents list yields
// ids, each detail carries the original text plus the metadata fields
// Capture stamped — scope, provenance, kind and audience round-trip
// unchanged so a replay into another provider loses nothing.
func (p *Provider) Export(ctx context.Context, scope memory.Scope) ([]memory.Entry, error) {
	if err := checkScopeKey(scope.Key); err != nil {
		return nil, err
	}
	bank := url.PathEscape(scope.Key)
	var out []memory.Entry
	const page = 100
	for offset := 0; ; offset += page {
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Total int `json:"total"`
		}
		path := fmt.Sprintf("/v1/default/banks/%s/documents?limit=%d&offset=%d", bank, page, offset)
		if err := p.do(ctx, http.MethodGet, path, nil, &list); err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			var doc struct {
				ID               string         `json:"id"`
				OriginalText     *string        `json:"original_text"`
				DocumentMetadata map[string]any `json:"document_metadata"`
				CreatedAt        string         `json:"created_at"`
			}
			if err := p.do(ctx, http.MethodGet, "/v1/default/banks/"+bank+"/documents/"+url.PathEscape(it.ID), nil, &doc); err != nil {
				return nil, err
			}
			e := memory.Entry{ID: doc.ID, Scope: scope}
			if doc.OriginalText != nil {
				e.Body = *doc.OriginalText
			}
			e.Provenance = memory.Provenance(metaString(doc.DocumentMetadata, mdProvenance))
			e.Kind = memory.Kind(metaString(doc.DocumentMetadata, mdKind))
			e.Audience = memory.Audience(metaString(doc.DocumentMetadata, mdAudience))
			e.SourceRef = metaString(doc.DocumentMetadata, mdSourceRef)
			if t, err := time.Parse(time.RFC3339, doc.CreatedAt); err == nil {
				e.ObservedAt = t
			}
			out = append(out, e)
		}
		if len(list.Items) == 0 || offset+len(list.Items) >= list.Total {
			return out, nil
		}
	}
}

// Search maps the recall endpoint onto the query lane — an empty bank is
// an empty slice, never an error.
func (p *Provider) Search(ctx context.Context, scope memory.Scope, q memory.Query) ([]memory.Hit, error) {
	if err := checkScopeKey(scope.Key); err != nil {
		return nil, err
	}
	body := map[string]any{"query": q.Text}
	var resp struct {
		Results []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"results"`
	}
	if err := p.do(ctx, http.MethodPost, "/v1/default/banks/"+url.PathEscape(scope.Key)+"/memories/recall", body, &resp); err != nil {
		return nil, err
	}
	hits := make([]memory.Hit, 0, len(resp.Results))
	for _, r := range resp.Results {
		hits = append(hits, memory.Hit{Ref: r.ID, Snippet: r.Text})
	}
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

// Recall has no honest wire mapping: the Hindsight reflect endpoint answers
// with generated text, not the raw evidence block the seam's pre-turn lane
// requires. Unsupported beats fabricated.
func (p *Provider) Recall(context.Context, memory.Scope) (memory.Block, error) {
	return memory.Block{}, memory.ErrUnsupported
}

// Write is the governed-file lane only the vault serves — a remote store
// has no hash-checked workspace file contract.
func (p *Provider) Write(context.Context, memory.WriteOp) (memory.WriteResult, error) {
	return memory.WriteResult{}, memory.ErrUnsupported
}

// Health probes GET /health, the database-verifying readiness endpoint —
// a live-but-degraded backend fails here rather than answering capture.
func (p *Provider) Health(ctx context.Context) error {
	return p.do(ctx, http.MethodGet, "/health", nil, nil)
}

// checkScopeKey refuses scope keys that could traverse the request path —
// defense in depth: MintScope output is hex, but the adapter enforces the
// contract regardless of how it was called.
func checkScopeKey(key string) error {
	if key == "" || strings.ContainsAny(key, "/\\?#%") || strings.Contains(key, "..") {
		return fmt.Errorf("hindsight: unsafe scope key %q", key)
	}
	return nil
}

func metaString(md map[string]any, key string) string {
	if md == nil {
		return ""
	}
	s, _ := md[key].(string)
	return s
}

// do executes one API call; any non-2xx is an honest error carrying the
// status, never an empty success.
func (p *Provider) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return fmt.Errorf("hindsight: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("hindsight: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
