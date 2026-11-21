package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestRenderDoesNotOpenToolHubStore(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("synthetic persona"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, dir, "alice", "r-0000000000000001")
	store := filepath.Join(dir, "runtime", "toolhub", "store.json")
	if err := os.MkdirAll(filepath.Dir(store), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("not a ToolHub store\n")
	if err := os.WriteFile(store, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(dir), dir, root, "prod"); err != nil {
		t.Fatalf("renderer consulted another module's store: %v", err)
	}
	got, err := os.ReadFile(store)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("renderer changed another module's store")
	}
}

// SPEC-0053 R1.4/D6: render emits generated/tool-manifest.<env>.json naming
// the expected tool surface per context — hub-projected capabilities under
// `projected`, Hermes-direct ones under `direct`.
func TestManifestRendered(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools = testTools("workspace", "telegram", "file=native", "web=toolhub", "notes=off")
	settings.Ingress = testIngress("workspace", "telegram")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:file", time.Hour)
	})
	if err := os.WriteFile(filepath.Join(d, "secrets.prod.env"), []byte("OPENAI_API_KEY=model\nTELEGRAM_BOT_TOKEN=bot\nTELEGRAM_ALLOWED_USERS=11\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(d, "generated", "tool-manifest.prod.json"))
	if err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	var manifest struct {
		Schema        int      `json:"schema"`
		Context       string   `json:"context"`
		PolicyVersion string   `json:"policy_version"`
		Projected     []string `json:"projected"`
		Direct        []string `json:"direct"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("manifest JSON: %v", err)
	}
	if manifest.Schema != 1 || manifest.Context != "alice" {
		t.Fatalf("manifest identity wrong: %+v", manifest)
	}
	rendered, err := ReadEnvironment(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PolicyVersion != policyVersion(rendered) {
		t.Fatalf("manifest policy %q != rendered %q", manifest.PolicyVersion, policyVersion(rendered))
	}
	if !slices.Contains(manifest.Projected, "web") || slices.Contains(manifest.Projected, "file") {
		t.Fatalf("projected surface wrong: %v", manifest.Projected)
	}
	if !slices.Contains(manifest.Direct, "file") || slices.Contains(manifest.Direct, "web") || slices.Contains(manifest.Direct, "notes") {
		t.Fatalf("direct surface wrong: %v", manifest.Direct)
	}
}

// SPEC-0053 R1.8/D7: render composes the effective SOUL — the owner's persona
// file plus the rendered channel contract — and the runtime mounts that
// effective file read-only, so the stable prompt prefix is versioned render
// output, not per-run bytes.
func TestStablePromptRendered(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	if err := os.WriteFile(filepath.Join(d, "SOUL.md"), []byte("# Persona\nSpeak plainly."), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	// The contract fragments are family-gated: files/routines/media/documents
	// wishes drive the sections this test asserts verbatim.
	settings.Tools = testTools("workspace", "telegram", "routines=toolhub", "files=toolhub", "documents=toolhub", "media=toolhub")
	settings.Ingress = testIngress("workspace", "telegram")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(context.Background(), testReader(d), d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(d, "generated", "soul-effective.prod.md"))
	if err != nil {
		t.Fatalf("effective SOUL missing: %v", err)
	}
	if info, err := os.Stat(filepath.Join(d, "generated", "soul-effective.prod.md")); err != nil || info.Mode().Perm()&0004 == 0 {
		t.Fatalf("effective SOUL must be readable by container uid: info=%v err=%v", info, err)
	}
	text := string(body)
	if !strings.Contains(text, "# Persona") || !strings.Contains(text, "Speak plainly.") {
		t.Fatalf("effective SOUL lost the persona: %q", text)
	}
	for _, want := range []string{"artifacts/documents/", "MEDIA: ", "routine_create", "file_list", "Snooze must be later than the actual next run"} {
		if !strings.Contains(text, want) {
			t.Fatalf("effective SOUL missing contract fragment %q", want)
		}
	}
	service := RuntimeService(settings, root, d)
	mounted := false
	for _, v := range service["volumes"].([]any) {
		volume, _ := v.(M)
		if volume["target"] == "/state/hermes/SOUL.md" && strings.Contains(fmt.Sprint(volume["source"]), "soul-effective.prod.md") && volume["read_only"] == true {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("runtime does not mount the effective SOUL: %+v", service["volumes"])
	}
}

// SPEC-0053 R1.4: the rendered runtime mounts the manifest read-only and
// points HUB_TOOL_MANIFEST at it, so the runtime's own surface probe reads
// the same expectation the operator rendered — in unmanaged and managed mode.
func TestRuntimeServiceMountsToolManifest(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	publishManagedRevision(t, d, "alice", "r-0000000000000001")
	settings, err := ReadEffective(context.Background(), testReader(d), d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Tools = testTools("workspace", "telegram")
	settings.Ingress = testIngress("workspace", "telegram")
	if err := saveSpace(t, d, settings); err != nil {
		t.Fatal(err)
	}
	service := RuntimeService(settings, root, d)
	env, _ := service["environment"].(M)
	if env["HUB_TOOL_MANIFEST"] != "/config/tool-manifest.json" {
		t.Fatalf("HUB_TOOL_MANIFEST=%v", env["HUB_TOOL_MANIFEST"])
	}
	// A published workspace passport serves the manifest inside the
	// /config view bind; without one the file mounts directly.
	found := false
	for _, v := range service["volumes"].([]any) {
		volume, _ := v.(M)
		if volume["target"] == "/config/tool-manifest.json" && volume["read_only"] == true {
			found = true
		}
		if volume["target"] == "/config" && volume["read_only"] == true &&
			strings.Contains(fmt.Sprint(volume["source"]), "config-view") {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest mount missing in %+v", service["volumes"])
	}
}
