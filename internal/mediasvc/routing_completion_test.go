package mediasvc

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRoutedFalPreservesResultFetchGuard(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fal-ai/test" {
			t.Errorf("unexpected provider path: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"images":[{"url":"https://not-allowed.invalid/result.png"}]}`)
	}))
	defer upstream.Close()
	c := Config{Engine: "fal", Engines: []string{"fal"}, Upstream: upstream.URL, UpstreamKey: "fixture", FetchHosts: []string{"allowed.invalid"}, Routes: map[string][]RouteHop{"portrait": {{Engine: "fal", Model: "fal-ai/test"}}}}
	engine, err := genEngine(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Generate(context.Background(), GenRequest{Model: "portrait", Prompt: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "fetch allowlist") {
		t.Fatalf("routed Fal lost its result-fetch guard: %v", err)
	}
}

func TestAsyncRoutingNeverRetriesAnUncertainSubmission(t *testing.T) {
	var receipts strings.Builder
	previous := log.Writer()
	log.SetOutput(&receipts)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, backend := range []string{"fal", "comfy"} {
		for _, outcome := range []string{"lost", "invalid", "missing-id", "server-error"} {
			var submitted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				submitted.Add(1)
				switch outcome {
				case "lost":
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
				case "invalid":
					_, _ = io.WriteString(w, `{`)
				case "missing-id":
					_, _ = io.WriteString(w, `{}`)
				case "server-error":
					http.Error(w, "unknown submission outcome", 500)
				}
			}))
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "portrait.json"), []byte(`{"6":{"inputs":{"text":"${PROMPT}"}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			primary, err := genEngine(Config{Engine: backend, Upstream: upstream.URL, QueueUpstream: upstream.URL, ModelDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			fallback := &fakeGen{}
			router, err := newGenRouter(map[string]GenEngine{"primary": primary, "fallback": fallback}, []string{"primary", "fallback"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = router.Submit(t.Context(), GenRequest{Kind: "video", Model: "portrait", Prompt: "fixture"})
			upstream.Close()
			if !strings.Contains(receipts.String(), "engine=primary submit failed") {
				t.Error("uncertain submission omitted its audit receipt")
			}
			receipts.Reset()
			if err == nil || !strings.Contains(err.Error(), "uncertain") || submitted.Load() != 1 || fallback.submits.Load() != 0 {
				t.Errorf("%s/%s repeated an uncertain paid job: primary=%d fallback=%d err=%v", backend, outcome, submitted.Load(), fallback.submits.Load(), err)
			}
		}
	}
}

func TestSynchronousRouterAlsoStopsAnUncertainQueuedGeneration(t *testing.T) {
	first := &fakeGen{err: errSubmissionUncertain}
	fallback := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	router, err := newGenRouter(map[string]GenEngine{"first": first, "fallback": fallback}, []string{"first", "fallback"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = router.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "uncertain") || fallback.lastReq.Model != "" {
		t.Error("synchronous image path repeated an uncertain queued submission")
	}
}

func TestMediaRoutesRejectAmbiguityUnboundedFallbackAndUnsafeIDs(t *testing.T) {
	for _, raw := range []string{"portrait=remote:first;portrait=remote:second", "portrait=" + strings.TrimSuffix(strings.Repeat("remote:model,", 9), ","), "portrait=remote:../admin", "portrait=remote:model?secret=x", "portrait=remote:" + strings.Repeat("a", 129), "portrait=remote:/admin", "portrait=remote:model%2fadmin", "portrait=remote:model space", "portrait=remote:model#fragment", "portrait=remote:model\nadmin"} {
		if _, err := parseRoutes(raw, []string{"remote"}); err == nil {
			t.Errorf("unsafe route accepted: %s", raw)
		}
	}
}
