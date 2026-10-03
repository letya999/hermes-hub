package stack

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/media"
	"gopkg.in/yaml.v3"
)

func TestDocumentImageProfile(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Model: "m", ModelURL: "http://model.invalid/v1", Features: []string{"workspace"}}
	cfg := Config(s)
	for _, platform := range []string{"cli", "telegram"} {
		toolsets := cfg["platform_toolsets"].(M)[platform].([]string)
		if slices.Contains(toolsets, "vision") || slices.Contains(toolsets, "image_gen") {
			t.Fatal(platform, toolsets)
		}
	}
	aux := cfg["auxiliary"].(M)["vision"].(M)
	if aux["provider"] != "main" || aux["timeout"] != 30 || aux["download_timeout"] != 15 || aux["max_concurrency"] != media.InspectConcurrency {
		t.Fatalf("auxiliary vision %#v", aux)
	}
	if _, ok := cfg["image_gen"]; ok {
		t.Fatal("image_gen section without the feature")
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil || strings.Contains(string(raw), "FAL_KEY") || !strings.Contains(string(raw), "${OPENAI_API_KEY}") {
		t.Fatalf("config leaked or lost the model reference: %v %s", err, raw)
	}
	s.ExecutionMode = "supervisor"
	supervised := Config(s)["platform_toolsets"].(M)["cli"].([]string)
	if slices.Contains(supervised, "cronjob") || slices.Contains(supervised, "vision") || slices.Contains(supervised, "image_gen") {
		t.Fatal(supervised)
	}

	s.ExecutionMode = ""
	s.Features = []string{"workspace", "image_gen"}
	cfg = Config(s)
	section := cfg["image_gen"].(M)
	if section["provider"] != media.ProviderCLIProxy || section["model"] != media.CLIProxyDefaultModel || section["delivery"] != media.DeliveryWorkspace {
		t.Fatal(section)
	}
	raw, err = yaml.Marshal(cfg)
	if err != nil || strings.Contains(string(raw), "FAL_KEY") || !strings.Contains(string(raw), media.CLIProxyDefaultModel) {
		t.Fatalf("granted config: %v %s", err, raw)
	}
	if _, _, ok, err := ServiceMCPConfig("image_gen"); err == nil || ok {
		t.Fatal("image_gen became self-service", ok, err)
	}
	if slices.Contains(selfEnvKeys(s), "FAL_KEY") || slices.Contains(selfEnvKeys(Settings{Features: []string{"workspace"}}), "FAL_KEY") {
		t.Fatal("cliproxy image_gen named FAL_KEY")
	}
	if strings.Contains(strings.Join(Doctor(s, map[string]string{"OPENAI_API_KEY": "present"}), " "), "FAL_KEY") {
		t.Fatal("cliproxy doctor asked for FAL_KEY")
	}
	s.ImageGen = media.ImageGen{Provider: media.FalProvider}
	cfg = Config(s)
	section = cfg["image_gen"].(M)
	if section["provider"] != media.FalProvider || section["model"] != media.ImageModel || section["delivery"] != media.DeliveryWorkspace {
		t.Fatal(section)
	}
	raw, err = yaml.Marshal(cfg)
	if err != nil || strings.Contains(string(raw), "FAL_KEY") || !strings.Contains(string(raw), media.ImageModel) {
		t.Fatalf("fal config: %v %s", err, raw)
	}
	if !slices.Contains(selfEnvKeys(s), "FAL_KEY") {
		t.Fatal("fal image_gen did not name FAL_KEY")
	}
	missing := strings.Join(Doctor(s, map[string]string{"OPENAI_API_KEY": "present"}), " ")
	if !strings.Contains(missing, "FAL_KEY") {
		t.Fatal(missing)
	}
	present := strings.Join(Doctor(s, map[string]string{"OPENAI_API_KEY": "present", "FAL_KEY": "present"}), " ")
	if strings.Contains(present, "FAL_KEY") {
		t.Fatal(present)
	}
	if err := (Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Features: []string{"image_gen"}, ImageGen: media.ImageGen{Provider: "guessed"}, BrowserPort: 6080, OAuthPort: 8000}).Validate(); err == nil {
		t.Fatal("unknown image provider accepted")
	}

	browser := Config(Settings{Features: []string{"browser"}, Model: "m", ModelURL: "http://model.invalid/v1"})
	args := browser["mcp_servers"].(M)["browser"].(M)["args"].([]string)
	if !slices.Contains(args, "vision,pdf") {
		t.Fatal(args)
	}

	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "hermes.yaml")
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(sourceDir, "effective.yaml")
	if err := MaterializeHermesConfig(source, dest, MaterializeOptions{ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true}); err != nil {
		t.Fatal(err)
	}
	effective, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(effective), media.ImageModel) || strings.Contains(string(effective), "FAL_KEY") {
		t.Fatalf("effective config: %v %s", err, effective)
	}

	envDir := t.TempDir()
	secrets := map[string]string{"OPENAI_API_KEY": "openai-sample", "FAL_KEY": "fal-sample-value"}
	if err := writeRuntimeEnvFiles(envDir, "prod", secrets, nil, []string{"FAL_KEY"}, map[string]string{"HUB_MEDIA_URL": "http://hub-media:8090"}); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(envDir, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") || !strings.Contains(string(runtimeEnv), "OPENAI_API_KEY=openai-sample") || !strings.Contains(string(runtimeEnv), "HUB_MEDIA_URL=http://hub-media:8090") {
		t.Fatalf("omitted runtime env: %v %s", err, runtimeEnv)
	}
	if err := writeRuntimeEnvFiles(envDir, "dev", secrets, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	included, err := os.ReadFile(filepath.Join(envDir, "runtime.dev.env"))
	if err != nil || !strings.Contains(string(included), "FAL_KEY=fal-sample-value") {
		t.Fatalf("included runtime env: %v %s", err, included)
	}

	space := t.TempDir()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config", "SOUL.md"), []byte("soul"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(space, "alice"); err != nil {
		t.Fatal(err)
	}
	settings, err := Read(filepath.Join(space, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "m"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Features = []string{"workspace"}
	if err := saveSettings(filepath.Join(space, "settings.yaml"), settings); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(space, "secrets.prod.env"), []byte("OPENAI_API_KEY=openai-sample\nFAL_KEY=fal-sample-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Render(space, repo); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"workspace/artifacts/documents", "workspace/artifacts/images"} {
		info, err := os.Stat(filepath.Join(space, filepath.FromSlash(rel)))
		if err != nil || !info.IsDir() {
			t.Fatal(rel, err)
		}
	}
	rendered, err := os.ReadFile(filepath.Join(space, "generated", "hermes.prod.yaml"))
	if err != nil || strings.Contains(string(rendered), "image_gen") || strings.Contains(string(rendered), "FAL_KEY") || strings.Contains(string(rendered), "fal-sample-value") {
		t.Fatalf("rendered without grant: %v %s", err, rendered)
	}
	runtimeEnv, err = os.ReadFile(filepath.Join(space, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") {
		t.Fatalf("rendered runtime leaked generation credential: %v %s", err, runtimeEnv)
	}
	settings.Features = []string{"workspace", "image_gen"}
	if err := saveSettings(filepath.Join(space, "settings.yaml"), settings); err != nil {
		t.Fatal(err)
	}
	if err := Render(space, repo); err != nil {
		t.Fatal(err)
	}
	rendered, err = os.ReadFile(filepath.Join(space, "generated", "hermes.prod.yaml"))
	if err != nil || !strings.Contains(string(rendered), "provider: "+media.ProviderCLIProxy) || !strings.Contains(string(rendered), media.CLIProxyDefaultModel) || strings.Contains(string(rendered), "fal-sample-value") {
		t.Fatalf("rendered cliproxy grant: %v %s", err, rendered)
	}
	runtimeEnv, err = os.ReadFile(filepath.Join(space, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") {
		t.Fatalf("cliproxy runtime included FAL_KEY: %v %s", err, runtimeEnv)
	}
	settings.ImageGen = media.ImageGen{Provider: media.FalProvider, Model: media.ModelFromChat, Delivery: media.DeliveryURL}
	if err := saveSettings(filepath.Join(space, "settings.yaml"), settings); err != nil {
		t.Fatal(err)
	}
	if err := Render(space, repo); err != nil {
		t.Fatal(err)
	}
	rendered, err = os.ReadFile(filepath.Join(space, "generated", "hermes.prod.yaml"))
	if err != nil || !strings.Contains(string(rendered), "provider: "+media.FalProvider) || !strings.Contains(string(rendered), "model: "+media.ModelFromChat) || !strings.Contains(string(rendered), "delivery: "+media.DeliveryURL) || strings.Contains(string(rendered), media.CLIProxyDefaultModel) || strings.Contains(string(rendered), "fal-sample-value") {
		t.Fatalf("rendered fal grant: %v %s", err, rendered)
	}
	// Provider credentials never touch env files: hub-media materializes the
	// broker grant at call time. The runtime keeps the grant but never the
	// key, compose carries the sidecar on the internal network with no
	// published port, and no media.<env>.env is written at all.
	runtimeEnv, err = os.ReadFile(filepath.Join(space, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") {
		t.Fatalf("rendered runtime leaked the fal credential: %v %s", err, runtimeEnv)
	}
	if _, err := os.Stat(filepath.Join(space, "media.prod.env")); !os.IsNotExist(err) {
		t.Fatalf("media provider env file must not be rendered: %v", err)
	}
	composeDoc, err := os.ReadFile(filepath.Join(space, "generated", "compose.prod.yaml"))
	if err != nil || !strings.Contains(string(composeDoc), "hub-media") || !strings.Contains(string(composeDoc), "HUB_MEDIA_URL") || strings.Contains(string(composeDoc), "fal-sample-value") {
		t.Fatalf("compose missing hub-media sidecar: %v %s", err, composeDoc)
	}
	mediaSvc := Compose(settings, repo, space)["services"].(M)["hub-media"].(M)
	mediaEnv := mediaSvc["environment"].(M)
	for _, want := range []string{"HUB_MEDIA_BROKER_GRANT", "HUB_CREDENTIAL_BROKER_MEDIA_URL", "HUB_CREDENTIAL_BROKER_MEDIA_KEY_FILE", "HUB_CREDENTIAL_BROKER_MEDIA_KEY_ID", "HUB_CREDENTIAL_BROKER_MEDIA_ISSUER", "HUB_CREDENTIAL_BROKER_MEDIA_CA_FILE", "HUB_PRINCIPAL_ID", "HUB_POLICY_VERSION"} {
		if mediaEnv[want] == nil || mediaEnv[want] == "" {
			t.Fatalf("hub-media missing broker env %s: %#v", want, mediaEnv)
		}
	}
	if mediaEnv["HUB_CREDENTIAL_BROKER_MEDIA_KEY_ID"] != "media" || mediaEnv["HUB_CREDENTIAL_BROKER_MEDIA_ISSUER"] != "hermes-media" {
		t.Fatalf("hub-media broker identity: %#v", mediaEnv)
	}
	for _, raw := range mediaSvc["env_file"].([]any) {
		if strings.Contains(raw.(M)["path"].(string), "media.prod.env") {
			t.Fatal("hub-media still mounts a provider env file")
		}
	}
	foundMediaKeys := false
	for _, raw := range mediaSvc["volumes"].([]any) {
		if raw.(M)["source"] == "broker-secrets-media" && raw.(M)["read_only"] == true {
			foundMediaKeys = true
		}
	}
	if !foundMediaKeys {
		t.Fatal("hub-media missing broker-secrets-media read-only mount")
	}
	if _, ok := Compose(settings, repo, space)["volumes"].(M)["broker-secrets-media"]; !ok {
		t.Fatal("compose missing broker-secrets-media volume")
	}
	// The runtime's env_file list must include media.auth, otherwise
	// HUB_MEDIA_AUTH never materializes inside the container.
	foundMediaAuth := false
	for _, raw := range Compose(settings, repo, space)["services"].(M)["hermes-runtime"].(M)["env_file"].([]any) {
		if raw.(M)["path"] == filepath.ToSlash(filepath.Join(space, "media.auth")) {
			foundMediaAuth = true
		}
	}
	if !foundMediaAuth {
		t.Fatal("runtime env_file missing media.auth")
	}

	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(caller)))
	integrations, err := os.ReadFile(filepath.Join(root, "docs", "integrations.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{media.HermesCommit, media.ImageModel, "docx", "webp", "not a live provider"} {
		if !strings.Contains(string(integrations), phrase) {
			t.Fatal("integrations missing", phrase)
		}
	}
	spec, err := os.ReadFile(filepath.Join(root, "specs", "active", "SPEC-0033-document-image-profile.md"))
	if err != nil || !strings.Contains(string(spec), media.ImageModel) || !strings.Contains(string(spec), "artifacts/documents") {
		t.Fatal(err)
	}
}
