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

func TestSecondaryMediaUsesUniqueSharedOwnerAuth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spaces")
	owner := filepath.Join(root, "infra")
	secondary := filepath.Join(root, "alice")
	for _, name := range []string{owner, secondary, filepath.Join(root, "other")} {
		if err := os.MkdirAll(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	s := Settings{Schema: 1, User: "infra", Timezone: "UTC", Model: "m", ModelURL: "http://model.invalid/v1", Tools: testTools("workspace", "image_gen"), Ingress: testIngress("workspace", "image_gen"), BrowserPort: 6080, OAuthPort: 8000}
	if err := saveSpace(t, owner, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "media.auth"), []byte("HUB_MEDIA_AUTH=shared-synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	no := false
	s.User, s.Infra, s.Environment = "alice", &no, "dev"
	if err := saveSpace(t, secondary, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondary, "media.auth"), []byte("HUB_MEDIA_AUTH=unused-local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err := runtimeMediaAuth(secondary, s)
	if err != nil || auth["HUB_MEDIA_AUTH"] != "shared-synthetic" {
		t.Fatal("secondary did not use shared media auth", err)
	}
	for _, file := range RuntimeService(s, root, secondary)["env_file"].([]any) {
		if file.(M)["path"] == filepath.ToSlash(filepath.Join(secondary, "media.auth")) {
			t.Fatal("secondary compose overrides shared auth with unused local token")
		}
	}
	if err := os.WriteFile(filepath.Join(owner, "media.auth"), []byte("HUB_MEDIA_AUTH=rotated-synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth, err = runtimeMediaAuth(secondary, s)
	if err != nil || auth["HUB_MEDIA_AUTH"] != "rotated-synthetic" {
		t.Fatal("rotation ignored", err)
	}
	other := s
	other.User, other.Infra = "other", nil
	otherDir := filepath.Join(root, "other")
	if err := os.MkdirAll(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveSpace(t, otherDir, other); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeMediaAuth(secondary, s); err == nil {
		t.Fatal("ambiguous shared owner accepted")
	}
	if err := os.RemoveAll(filepath.Join(root, "other")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(owner, "media.auth")); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeMediaAuth(secondary, s); err == nil {
		t.Fatal("missing shared auth fell back to local")
	}
	if err := os.Mkdir(filepath.Join(owner, "media.auth"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeMediaAuth(secondary, s); err == nil {
		t.Fatal("non-file shared auth accepted")
	}
}

func TestPerSpaceMediaUsesOwnAuthAndModelRelay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spaces")
	owner, secondary := filepath.Join(root, "infra"), filepath.Join(root, "alice")
	for _, path := range []string{owner, secondary} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owner, "media.auth"), []byte("HUB_MEDIA_AUTH=other-owner\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondary, "media.auth"), []byte("HUB_MEDIA_AUTH=private-media\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := profileSettings("workspace", "image_gen")
	no := false
	s.User, s.Infra = "alice", &no
	s.PassportServices = map[string]string{"hub-media": "per-space", "model-relay": "per-space", "hermes-runtime": "supervised"}
	s.ImageGen = media.ImageGen{Provider: media.ProviderCLIProxy, Model: "gemini-3.1-flash-image"}
	auth, err := runtimeMediaAuth(secondary, s)
	if err != nil || auth["HUB_MEDIA_AUTH"] != "private-media" {
		t.Fatalf("per-space media used another owner: %v", err)
	}
	services := Compose(s, t.TempDir(), secondary)["services"].(M)
	if _, ok := services["hub-media"]; ok {
		t.Fatal("per-space media acquired shared service name")
	}
	svc := services["hub-media-alice"].(M)
	if got := svc["healthcheck"].(M)["test"].([]string); !slices.Equal(got, []string{"CMD", "hub-media", "health-local"}) {
		t.Fatalf("per-space media healthcheck = %v", got)
	}
	env := svc["environment"].(M)
	if env["HUB_MEDIA_UPSTREAM"] != "http://model-relay-alice:8318" || env["HUB_MEDIA_IMAGE_MODEL"] != "gemini-3.1-flash-image" || env["HUB_MEDIA_MODEL_ROUTES"] != "gemini-3.1-flash-image=remote:image" || env["HUB_MEDIA_CHAT_MODELS"] != "image" || env["HUB_MEDIA_BROKER_GRANT"] != "" {
		t.Fatal("per-space media route or grant is wrong")
	}
	if _, ok := env["HUB_MEDIA_UPSTREAM_KEY"]; ok {
		t.Fatal("model relay bearer entered Compose environment")
	}
	files := svc["env_file"].([]any)
	if len(files) != 2 || !strings.HasSuffix(files[1].(M)["path"].(string), "media-relay.auth") {
		t.Fatal("per-space media lacks its private relay bearer file")
	}
	for _, raw := range svc["volumes"].([]any) {
		if raw.(M)["source"] == "broker-secrets-media" {
			t.Fatal("per-space media mounted the shared Broker identity")
		}
	}
	if !strings.Contains(strings.Join(managedEgressHosts(s, secondary), ","), "hub-media-alice") {
		t.Fatal("per-space media is unreachable from managed runtime")
	}
	if got := RuntimeService(s, root, secondary)["environment"].(M)["HUB_MEDIA_URL"]; got != "http://hub-media-alice:8090" {
		t.Fatalf("runtime media URL = %v", got)
	}
	unsupported := s
	unsupported.ImageGen.Model = "gpt-image-2"
	if err := unsupported.Validate(); err == nil {
		t.Fatal("per-space relay accepted a non-chat image endpoint")
	}
}

// A managed infra owner carried only by its published revision still owns the
// shared media auth: the consumer's scan resolves siblings through the
// managed-aware reader (SPEC-0092 R8.1).
func TestSharedMediaAuthReadsManagedOwnerRevision(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spaces")
	owner := filepath.Join(root, "infra")
	secondary := filepath.Join(root, "alice")
	for _, dir := range []string{owner, secondary} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owner, "agent.yaml"), []byte(
		"schema: 3\nuser: infra\ntimezone: UTC\ncapability_profile_id: infra-default\ncapability_generation: 1\nmodel: m\nmodel_url: http://model-relay:8318/v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	docs := managedDocs("infra")
	docs["tools"] = "kind: tools\ncore:\n  discovery: {via: toolhub}\n  image_gen: {via: toolhub}\n"
	publishManagedDocs(t, owner, "infra", "r-0123456789abcdef", docs)
	if err := os.WriteFile(filepath.Join(owner, "media.auth"), []byte("HUB_MEDIA_AUTH=shared-synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	no := false
	s := Settings{Schema: 1, User: "alice", Infra: &no, Timezone: "UTC", Model: "m", ModelURL: "http://model.invalid/v1", Tools: testTools("workspace", "image_gen"), Ingress: testIngress("workspace", "image_gen"), BrowserPort: 6080, OAuthPort: 8000, Environment: "dev"}
	if err := saveSpace(t, secondary, s); err != nil {
		t.Fatal(err)
	}
	auth, err := runtimeMediaAuth(secondary, s)
	if err != nil || auth["HUB_MEDIA_AUTH"] != "shared-synthetic" {
		t.Fatalf("secondary did not resolve the managed owner's shared auth: %v", err)
	}
}

func TestMediaProfileBuildsSharedImageOnce(t *testing.T) {
	for _, environment := range []string{"dev", "prod"} {
		t.Run(environment, func(t *testing.T) {
			s := profileSettings("workspace", "image_gen")
			s.Environment = environment
			services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
			core := services["cliproxy"].(M)
			mediaSvc := services["hub-media"].(M)
			if mediaSvc["image"] != core["image"] || core["build"] == nil {
				t.Fatal("media must consume the explicitly built shared core image")
			}
			for name, raw := range services {
				svc := raw.(M)
				if name != "cliproxy" && svc["image"] == core["image"] && svc["build"] != nil {
					t.Fatalf("%s builds the shared core tag a second time", name)
				}
			}
		})
	}
}

func TestDocumentImageProfile(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Model: "m", ModelURL: "http://model.invalid/v1", Tools: testTools("workspace", "image_gen"), Ingress: testIngress("workspace", "image_gen")}
	if _, _, ok, err := ServiceMCPConfig("image_gen"); err == nil || ok {
		t.Fatal("image_gen became self-service", ok, err)
	}
	if slices.Contains(selfEnvKeys(s), "FAL_KEY") || slices.Contains(selfEnvKeys(Settings{Tools: testTools("workspace"), Ingress: testIngress("workspace")}), "FAL_KEY") {
		t.Fatal("cliproxy image_gen named FAL_KEY")
	}
	if strings.Contains(strings.Join(Doctor(s, map[string]string{"OPENAI_API_KEY": "present"}), " "), "FAL_KEY") {
		t.Fatal("cliproxy doctor asked for FAL_KEY")
	}
	s.ImageGen = media.ImageGen{Provider: media.FalProvider}
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
	if err := (Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Tools: testTools("image_gen"), Ingress: testIngress("image_gen"), ImageGen: media.ImageGen{Provider: "guessed"}, BrowserPort: 6080, OAuthPort: 8000}).Validate(); err == nil {
		t.Fatal("unknown image provider accepted")
	}

	envDir := t.TempDir()
	secrets := map[string]string{"OPENAI_API_KEY": "openai-sample", "FAL_KEY": "fal-sample-value"}
	if err := writeRuntimeEnvFiles(envDir, "prod", secrets, nil, []string{"FAL_KEY"}, map[string]string{"HUB_MEDIA_URL": "http://hub-media:8090"}); err != nil {
		t.Fatal(err)
	}
	runtimeEnv, err := os.ReadFile(filepath.Join(envDir, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") || !strings.Contains(string(runtimeEnv), "OPENAI_API_KEY=openai-sample") || !strings.Contains(string(runtimeEnv), "HUB_MEDIA_URL=http://hub-media:8090") || !strings.Contains(string(runtimeEnv), "HUB_COMMUNICATION_CONTROL_URL=http://communication-hub:8081") {
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
	publishManagedRevision(t, space, "alice", "r-0000000000000001")
	settings, err := ReadEffective(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "m"
	settings.ModelURL = "http://model.invalid/v1"
	// hub-media is an infra-plane sidecar: the space must carry infra shape
	// for the composed service to exist, so the fixture pins infra: true.
	infra := true
	settings.Infra = &infra
	settings.Tools, settings.Ingress = testTools("workspace"), testIngress("workspace")
	if err := saveSpace(t, space, settings); err != nil {
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
	var renderedDoc map[string]any
	_ = yaml.Unmarshal(rendered, &renderedDoc)
	_, hasImageSection := renderedDoc["image_gen"]
	if err != nil || hasImageSection || strings.Contains(string(rendered), "FAL_KEY") || strings.Contains(string(rendered), "fal-sample-value") {
		t.Fatalf("rendered without grant: %v %s", err, rendered)
	}
	runtimeEnv, err = os.ReadFile(filepath.Join(space, "runtime.prod.env"))
	if err != nil || strings.Contains(string(runtimeEnv), "FAL_KEY") || strings.Contains(string(runtimeEnv), "fal-sample-value") {
		t.Fatalf("rendered runtime leaked generation credential: %v %s", err, runtimeEnv)
	}
	settings.Tools, settings.Ingress = testTools("workspace", "image_gen"), testIngress("workspace", "image_gen")
	if err := saveSpace(t, space, settings); err != nil {
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
	if err := saveSpace(t, space, settings); err != nil {
		t.Fatal(err)
	}
	// The compose probes below must read the just-published revision: the
	// stack passport now declares hub-media and config carries the fal grant.
	settings, err = ReadEffective(space, "prod")
	if err != nil {
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
	if err != nil || !strings.Contains(string(composeDoc), "hub-media") || strings.Contains(string(composeDoc), "fal-sample-value") {
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
	// Engine ladder + model routes ride the deploy env, same contract as the
	// other HUB_MEDIA_* knobs — the sidecar never sees literal credentials.
	for _, want := range []string{"HUB_MEDIA_ENGINES", "HUB_MEDIA_MODEL_ROUTES", "HUB_MEDIA_COMMAND", "HUB_MEDIA_PROVIDERS"} {
		v, ok := mediaEnv[want].(string)
		if !ok || !strings.Contains(v, want) {
			t.Fatalf("hub-media missing %s passthrough: %#v", want, mediaEnv[want])
		}
	}
	// A managed space has no resident hermes-runtime service: the spawned
	// runtime's service contract lives in managed-runtime.<env>.env, which
	// must carry the media endpoint and bearer or the runtime can never
	// authenticate to the sidecar. The auth file itself is rendered beside it.
	managedEnv, err := os.ReadFile(filepath.Join(space, "managed-runtime.prod.env"))
	if err != nil || !strings.Contains(string(managedEnv), "HUB_MEDIA_URL=http://hub-media:8090") || !strings.Contains(string(managedEnv), "HUB_MEDIA_AUTH=") || strings.Contains(string(managedEnv), "fal-sample-value") {
		t.Fatalf("spawned runtime lost the media service contract: %v %s", err, managedEnv)
	}
	if _, err := os.Stat(filepath.Join(space, "media.auth")); err != nil {
		t.Fatalf("media.auth not rendered for the spawned runtime: %v", err)
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

// The r5 `media` toolset is the canonical admission for an image_gen operator
// grant; the managed check must not demand the retired legacy entry name.
func TestManagedImageGenAcceptsMediaToolset(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", Model: "m", ModelURL: "http://model-relay:8318/v1", BrowserPort: 6080, OAuthPort: 8000, CapabilityProfileID: "p", CapabilityGeneration: 1, Tools: testTools("media=toolhub"), ImageGen: media.ImageGen{Provider: media.ProviderCLIProxy}}
	if err := s.Validate(); err != nil {
		t.Fatalf("r5 media toolset rejected for image_gen grant: %v", err)
	}
}
