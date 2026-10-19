package api

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDockerfileBranches(t *testing.T) {
	launch, conn, _, err := parseDockerfile([]byte(`
# comment
FROM example/mcp@sha256:abcd
ENV API_KEY
ENV UNUSED lowercase
ENTRYPOINT ["node", "server.js"]
HEALTHCHECK CMD ["curl", "-f", "http://localhost/health"]
VOLUME ["/state"]
`), "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Transport != ContainerMCP || len(launch.Entrypoint) < 2 || launch.Entrypoint[0] != "node" {
		t.Fatalf("launch: %+v", launch)
	}
	if len(launch.Mounts) != 1 || launch.Mounts[0].Target != "/state" {
		t.Fatalf("mounts: %+v", launch.Mounts)
	}
	if launch.Health.Kind != "exec" || !strings.Contains(launch.Health.Value, "curl") {
		t.Fatalf("health: %+v", launch.Health)
	}
	foundKey := false
	for _, f := range conn.Fields {
		if f.Name == "API_KEY" {
			foundKey = true
		}
	}
	if !foundKey {
		t.Fatalf("env credential missing: %+v", conn.Fields)
	}
	if len(launch.Evidence) < 2 {
		t.Fatalf("evidence: %+v", launch.Evidence)
	}
	_, _, w, err := parseDockerfile([]byte("FROM mutable:latest\nRUN --mount=type=bind,source=x go build\n"), "Dockerfile")
	if err != nil || len(w) == 0 {
		t.Fatalf("unsafe RUN mount accepted: %v %v", err, w)
	}
	_, _, w, err = parseDockerfile([]byte("VOLUME /var/run/docker.sock\n"), "Dockerfile")
	if err != nil || len(w) == 0 {
		t.Fatalf("docker.sock volume accepted: %v %v", err, w)
	}
	l2, _, w, err := parseDockerfile([]byte("FROM mutable:latest\nCMD npm start\n"), "Dockerfile")
	if err != nil || len(l2.Entrypoint) != 0 || len(w) != 2 {
		t.Fatalf("mutable base + shell cmd: %+v %v", l2, w)
	}
}

func TestParseComposeBranches(t *testing.T) {
	doc := map[string]any{"services": map[string]any{
		"mcp-server": map[string]any{
			"image":       "example/mcp@sha256:" + strings.Repeat("b", 64),
			"command":     []any{"--serve"},
			"environment": map[string]any{"API_KEY": "x", "DEBUG": nil},
			"volumes":     []any{"data:/data:ro"},
			"secrets":     []any{"SVC_SECRET"},
			"healthcheck": map[string]any{"test": []any{"CMD", "wget", "-q"}},
		},
		"redis": map[string]any{"image": "redis:latest"},
	}}
	launch, conn, warnings, err := ParseComposeDocument(doc, "compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if launch.Artifact != "example/mcp" || launch.Digest == "" {
		t.Fatalf("artifact/digest: %+v", launch)
	}
	if len(launch.Sidecars) != 1 || launch.Sidecars[0] != "redis" {
		t.Fatalf("sidecars: %+v", launch.Sidecars)
	}
	if len(launch.Mounts) != 1 || !launch.Mounts[0].ReadOnly {
		t.Fatalf("mounts: %+v", launch.Mounts)
	}
	if launch.Health.Kind != "exec" {
		t.Fatalf("health: %+v", launch.Health)
	}
	var secretField bool
	for _, f := range conn.Fields {
		if f.Name == "SVC_SECRET" && f.Secret {
			secretField = true
		}
	}
	if !secretField {
		t.Fatalf("secret env field missing: %+v", conn.Fields)
	}
	if len(warnings) == 0 {
		t.Fatal("sidecar warning missing")
	}
	if _, _, _, err := parseCompose([]byte("{"), "c.yaml"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid yaml: %v", err)
	}
	_, _, w, err := ParseComposeDocument(map[string]any{}, "c.yaml")
	if err != nil || len(w) == 0 {
		t.Fatalf("empty services: %v %v", err, w)
	}
	priv := map[string]any{"services": map[string]any{"mcp": map[string]any{"image": "x@sha256:" + strings.Repeat("b", 64), "privileged": true}}}
	if _, _, _, err := ParseComposeDocument(priv, "c.yaml"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("privileged compose: %v", err)
	}
	mutable := map[string]any{"services": map[string]any{"mcp": map[string]any{"image": "x:latest"}}}
	if _, _, _, err := ParseComposeDocument(mutable, "c.yaml"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable image: %v", err)
	}
	// Several services sharing one MCP-named image are not ambiguous when the
	// service name itself selects exactly one MCP process (unified server +
	// per-source siblings on the same image).
	shared := map[string]any{"services": map[string]any{
		"marketplace-mcp": map[string]any{"build": ".", "image": "ru-marketplace-mcp:2.5.3", "command": []any{"marketplace-mcp"}},
		"ozon":            map[string]any{"build": ".", "image": "ru-marketplace-mcp:2.5.3", "command": []any{"ozon-mcp"}},
	}}
	launch, _, _, err = ParseComposeDocument(shared, "c.yaml")
	if err != nil {
		t.Fatalf("shared-image compose rejected: %v", err)
	}
	if len(launch.Args) != 1 || launch.Args[0] != "marketplace-mcp" {
		t.Fatalf("wrong service selected: %+v", launch)
	}
	hostMount := map[string]any{"services": map[string]any{"mcp": map[string]any{"image": "x@sha256:" + strings.Repeat("b", 64), "volumes": []any{"/etc:/mnt"}}}}
	if _, _, _, err := ParseComposeDocument(hostMount, "c.yaml"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("host mount: %v", err)
	}
}
