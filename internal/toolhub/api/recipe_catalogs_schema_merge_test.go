package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogSchemaAndFetch(t *testing.T) {
	// schema-derived fields keep their json target and required flag
	recipe := ConnectionFromSchema(map[string]any{
		"properties": map[string]any{
			"apiKey":   map[string]any{"format": "password"},
			"region":   map[string]any{"type": "string"},
			"not env!": map[string]any{},
		},
		"required": []any{"apiKey"},
	}, "alt")
	if len(recipe.Fields) < 2 {
		t.Fatalf("schema fields: %+v", recipe.Fields)
	}
	var key ConnectionField
	for _, f := range recipe.Fields {
		if f.Name == "API_KEY" {
			key = f
		}
	}
	if !key.Required || !key.Secret || key.Target != "apiKey" || key.Alternative != "alt" {
		t.Fatalf("key field: %+v", key)
	}
	// docker-mcp entries: secrets env names + config schema merge
	entry := map[string]any{
		"secrets": []any{map[string]any{"env": "API_TOKEN"}, "bad", map[string]any{"env": "lower"}},
		"config":  map[string]any{"properties": map[string]any{"teamId": map[string]any{}}},
	}
	recipe = ConnectionFromDockerEntry(entry)
	if len(recipe.Fields) != 2 {
		t.Fatalf("docker fields: %+v", recipe.Fields)
	}
	// catalog query: https/http only, no userinfo, values merged
	q, err := CatalogQuery("https://registry.example.com/v0?a=1", map[string]string{"q": "x y"})
	if err != nil || q != "https://registry.example.com/v0?a=1&q=x+y" {
		t.Fatalf("query: %q %v", q, err)
	}
	if _, err := CatalogQuery("ftp://x", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("scheme")
	}
	if _, err := CatalogQuery("https://u:p@x/y", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("userinfo")
	}
	// fetch: ok body, 404 marker header, bad status, invalid JSON
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth") != "t" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ok.Close()
	body, _, err := FetchCatalogJSON(t.Context(), nil, ok.URL, http.Header{"X-Auth": {"t"}}, 1<<20)
	if err != nil || string(body) != `{"ok":true}` {
		t.Fatalf("fetch: %v %v", string(body), err)
	}
	nf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer nf.Close()
	_, hdr, err := FetchCatalogJSON(t.Context(), nil, nf.URL, nil, 1<<20)
	if err != nil || hdr.Get("X-Catalog-Not-Found") != "1" {
		t.Fatalf("404 marker: %v %v", hdr, err)
	}
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer bs.Close()
	if _, _, err := FetchCatalogJSON(t.Context(), nil, bs.URL, nil, 1<<20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("status: %v", err)
	}
	bj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }))
	defer bj.Close()
	if _, _, err := FetchCatalogJSON(t.Context(), nil, bj.URL, nil, 1<<20); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad json: %v", err)
	}
}
