package mediasvc

import (
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestMediaRoutesParseFromEnv(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	t.Setenv("HUB_MEDIA_DATA", t.TempDir())
	t.Setenv("HUB_MEDIA_ENGINE", "remote")
	t.Setenv("HUB_MEDIA_ENGINES", "remote,fal")
	t.Setenv("HUB_MEDIA_UPSTREAM", "http://cliproxy:8317")
	t.Setenv("HUB_MEDIA_MODEL_ROUTES", "portrait=remote:gpt-image-2,fal:fal-ai/flux;clip=fal:fal-ai/wan")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "portrait")
	t.Setenv("HUB_MEDIA_IMAGE_MODELS", "portrait")
	t.Setenv("FAL_KEY", "k")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Engines) != 2 || c.Engines[0] != "remote" || c.Engines[1] != "fal" {
		t.Fatalf("engines: %v", c.Engines)
	}
	route := c.Routes["portrait"]
	if len(route) != 2 || route[0].Engine != "remote" || route[0].Model != "gpt-image-2" || route[1].Engine != "fal" || route[1].Model != "fal-ai/flux" {
		t.Fatalf("route: %+v", route)
	}
	if len(c.Routes["clip"]) != 1 || c.Routes["clip"][0].Model != "fal-ai/wan" {
		t.Fatalf("clip route: %+v", c.Routes["clip"])
	}
	for _, bad := range []string{
		"x=unknown:m",      // engine not in the engine list
		"x=remote",         // no provider model id
		"x=remote:",        // empty provider id
		"=remote:m",        // no logical name
		"x=remote:m;y=fal", // second entry malformed
	} {
		t.Setenv("HUB_MEDIA_MODEL_ROUTES", bad)
		if _, err := ConfigFromEnv(RoleMedia); err == nil {
			t.Fatalf("malformed routes accepted: %q", bad)
		}
	}
}

func TestRouterFallsThroughAndMapsProviderID(t *testing.T) {
	local := &fakeGen{err: errors.New("local down")}
	remote := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	r, err := newGenRouter(
		map[string]GenEngine{"local": local, "remote": remote},
		[]string{"local", "remote"},
		map[string][]RouteHop{"portrait": {{Engine: "local", Model: "sdxl"}, {Engine: "remote", Model: "gpt-image-2"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "a cat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Data) == 0 || remote.lastReq.Model != "gpt-image-2" || local.lastReq.Model != "sdxl" {
		t.Fatalf("route fell through with wrong ids: local=%q remote=%q", local.lastReq.Model, remote.lastReq.Model)
	}
	// Unrouted model ids pass through unchanged to engines in order.
	local.err = nil
	local.result = GenResult{Data: testPNG, Mime: "image/png"}
	local.lastReq, remote.lastReq = GenRequest{}, GenRequest{}
	out, err = r.Generate(t.Context(), GenRequest{Kind: "image", Model: "gpt-image-2", Prompt: "x"})
	if err != nil || local.lastReq.Model != "gpt-image-2" || remote.lastReq.Model != "" {
		t.Fatalf("unrouted model bypassed the engine order: local=%q remote=%q", local.lastReq.Model, remote.lastReq.Model)
	}
}

func TestRouterLogsEveryHopAttempt(t *testing.T) {
	var logs strings.Builder
	orig := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(orig) })
	local := &fakeGen{err: errors.New("local down")}
	remote := &fakeGen{result: GenResult{Data: testPNG, Mime: "image/png"}}
	r, err := newGenRouter(
		map[string]GenEngine{"local": local, "remote": remote},
		[]string{"local", "remote"},
		map[string][]RouteHop{"portrait": {{Engine: "local", Model: "sdxl"}, {Engine: "remote", Model: "gpt-image-2"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	if !strings.Contains(out, "engine=local") || !strings.Contains(out, "engine=remote") {
		t.Fatalf("missing per-hop audit receipt: %q", out)
	}
}

func TestMediaCommandEngineFromEnv(t *testing.T) {
	t.Setenv("HUB_MEDIA_AUTH", "tok")
	t.Setenv("HUB_MEDIA_DATA", t.TempDir())
	t.Setenv("HUB_MEDIA_ENGINE", "command")
	t.Setenv("HUB_MEDIA_COMMAND", "/bin/echo")
	t.Setenv("HUB_MEDIA_IMAGE_MODEL", "sdxl")
	c, err := ConfigFromEnv(RoleMedia)
	if err != nil {
		t.Fatalf("command engine rejected for media role: %v", err)
	}
	if len(c.Engines) != 1 || c.Engines[0] != "command" {
		t.Fatalf("engines: %v", c.Engines)
	}
	// Missing command must still fail closed.
	t.Setenv("HUB_MEDIA_COMMAND", "")
	if _, err := ConfigFromEnv(RoleMedia); err == nil {
		t.Fatal("command engine without HUB_MEDIA_COMMAND accepted")
	}
}

func TestGenEngineBuildsRouterFromEngineList(t *testing.T) {
	cmd := scriptFile(t, "gen",
		"#!/bin/sh\ncat >/dev/null\nprintf local-bytes\n",
		"@echo off\r\nmore\r\necho local-bytes\r\n")
	cfg := Config{
		Role: RoleMedia, Engine: "command", Command: cmd,
		Engines: []string{"command", "fal"}, UpstreamKey: "k",
		Routes: map[string][]RouteHop{"portrait": {{Engine: "command", Model: "sdxl"}, {Engine: "fal", Model: "fal-ai/flux"}}},
	}
	eng, err := genEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eng.(*genRouter); !ok {
		t.Fatalf("engine list did not build a router: %T", eng)
	}
	out, err := eng.Generate(t.Context(), GenRequest{Kind: "image", Model: "portrait", Prompt: "x"})
	if err != nil || !strings.Contains(string(out.Data), "local-bytes") {
		t.Fatalf("routed generate: %v %q", err, out.Data)
	}
}

func TestCommandGenRunsLocalModel(t *testing.T) {
	cmd := scriptFile(t, "gen",
		"#!/bin/sh\ncat >/dev/null\nprintf '%s' \"${HUB_MEDIA_REQUEST_MODEL}:${HUB_MEDIA_REQUEST_KIND}\"\n",
		"@echo off\r\nmore\r\necho %HUB_MEDIA_REQUEST_MODEL%:%HUB_MEDIA_REQUEST_KIND%\r\n")
	eng := commandGen{command: cmd}
	out, err := eng.Generate(t.Context(), GenRequest{Kind: "image", Model: "sdxl", Prompt: "a cat"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Data), "sdxl:image") {
		t.Fatalf("command engine did not receive model/kind env: %q", out.Data)
	}
	if !eng.Ready(t.Context()) {
		t.Fatal("command gen not ready")
	}
	bad := scriptFile(t, "badgen", "#!/bin/sh\nexit 1\n", "@echo off\r\nexit /b 1\r\n")
	if _, err := (commandGen{command: bad}).Generate(t.Context(), GenRequest{Kind: "image", Model: "m", Prompt: "x"}); err == nil {
		t.Fatal("failing command generated")
	}
}

func TestRouterSubmitSkipsSyncEnginesAndRecordsEngine(t *testing.T) {
	syncOnly := &fakeGenNoAsync{}
	async := &fakeGen{result: GenResult{Data: testMP4, Mime: "video/mp4"}, pollDone: true}
	r, err := newGenRouter(
		map[string]GenEngine{"local": syncOnly, "fal": async},
		[]string{"local", "fal"},
		map[string][]RouteHop{"clip": {{Engine: "local", Model: "local-vid"}, {Engine: "fal", Model: "fal-ai/wan"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	eng, ok := any(r).(interface {
		Submit(ctx context.Context, req GenRequest) (SubmitHandle, error)
		Poll(ctx context.Context, h SubmitHandle, model string) (GenResult, bool, error)
	})
	if !ok {
		t.Fatal("router does not expose an engine-bound async submit/poll")
	}
	h, err := eng.Submit(t.Context(), GenRequest{Kind: "video", Model: "clip", Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if h.Engine != "fal" || async.lastSubmit.Model != "fal-ai/wan" {
		t.Fatalf("submit went to the wrong engine: %+v", h)
	}
	// Poll resumes on the recorded engine, not the route order.
	out, done, err := eng.Poll(t.Context(), h, "clip")
	if err != nil || !done || len(out.Data) == 0 || async.lastPollID != h.RemoteID {
		t.Fatalf("poll lost its engine binding: %+v", h)
	}
}
