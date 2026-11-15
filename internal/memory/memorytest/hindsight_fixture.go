package memorytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"testing"
)

// HindsightFixture is an in-memory stand-in for the Hindsight HTTP API
// verified at upstream pin 869228c — retain upserts documents by
// document_id and lands item metadata as document_metadata, documents list
// paginates, recall returns every retained fact for the bank, /health
// flips with SetDown. Contract-level only: fixture verification is never a
// live-deployment claim.
type HindsightFixture struct {
	mu   sync.Mutex
	down bool
	// bank -> document_id -> doc
	docs map[string]map[string]map[string]any
	// observed retain payloads for wire assertions
	retained []map[string]any
}

// NewHindsightFixture serves the fixture until the test ends; callers build
// the adapter with hindsight.New(srv.URL).
func NewHindsightFixture(t *testing.T) (*HindsightFixture, *httptest.Server) {
	t.Helper()
	fx := &HindsightFixture{docs: map[string]map[string]map[string]any{}}
	srv := httptest.NewServer(fx.handler())
	t.Cleanup(srv.Close)
	return fx, srv
}

// SetDown flips the /health endpoint between 200 and 503.
func (f *HindsightFixture) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Docs returns the retained documents of one bank — the wire-level store
// for assertions that must not route through the adapter.
func (f *HindsightFixture) Docs(bank string) map[string]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]map[string]any{}
	for k, v := range f.docs[bank] {
		out[k] = v
	}
	return out
}

// SetDocs seeds a bank's document store directly — for tests that need a
// pre-populated provider without routing through retain.
func (f *HindsightFixture) SetDocs(bank string, docs map[string]map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[bank] = docs
}

// Retained returns the raw retain request bodies the adapter sent.
func (f *HindsightFixture) Retained() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.retained...)
}

func (f *HindsightFixture) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/default/banks/{bank}/memories", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req struct {
			Items []struct {
				Content    string            `json:"content"`
				Context    string            `json:"context"`
				DocumentID string            `json:"document_id"`
				Timestamp  string            `json:"timestamp"`
				Metadata   map[string]string `json:"metadata"`
			} `json:"items"`
			Async bool `json:"async"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bank := r.PathValue("bank")
		if f.docs[bank] == nil {
			f.docs[bank] = map[string]map[string]any{}
		}
		raw, _ := json.Marshal(req)
		var observed map[string]any
		_ = json.Unmarshal(raw, &observed)
		f.retained = append(f.retained, observed)
		for _, it := range req.Items {
			// document_id re-retained => upsert replaces, never duplicates.
			f.docs[bank][it.DocumentID] = map[string]any{
				"id":                it.DocumentID,
				"original_text":     it.Content,
				"document_metadata": it.Metadata,
				"created_at":        it.Timestamp,
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "bank_id": bank, "items_count": len(req.Items), "async": false,
		})
	})
	mux.HandleFunc("GET /v1/default/banks/{bank}/documents", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var all []map[string]any
		for _, d := range f.docs[r.PathValue("bank")] {
			all = append(all, map[string]any{"id": d["id"]})
		}
		sort.Slice(all, func(i, j int) bool { return all[i]["id"].(string) < all[j]["id"].(string) })
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 {
			limit = 100
		}
		end := offset + limit
		if offset > len(all) {
			offset = len(all)
		}
		if end > len(all) {
			end = len(all)
		}
		items := all[offset:end]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": items, "total": len(all), "limit": limit, "offset": offset,
		})
	})
	mux.HandleFunc("GET /v1/default/banks/{bank}/documents/{doc}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		d, ok := f.docs[r.PathValue("bank")][r.PathValue("doc")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(d)
	})
	mux.HandleFunc("POST /v1/default/banks/{bank}/memories/recall", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var results []map[string]any
		for _, d := range f.docs[r.PathValue("bank")] {
			results = append(results, map[string]any{
				"id": d["id"].(string) + "-fact", "text": d["original_text"],
				"document_id": d["id"],
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	})
	return mux
}
