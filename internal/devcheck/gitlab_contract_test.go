package devcheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type gitlabJob struct {
	Stage         string
	Needs         []string
	Script        []string
	ResourceGroup string `yaml:"resource_group"`
	Rules         []struct{ If, When string }
	Parallel      struct{ Matrix []map[string]string }
}

func gitlabJobs(t *testing.T) map[string]gitlabJob {
	t.Helper()
	data, err := os.ReadFile("../../.gitlab-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var nodes map[string]yaml.Node
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]gitlabJob{}
	for _, name := range []string{"test-job", "build-image", "deploy"} {
		node, ok := nodes[name]
		if !ok {
			t.Fatalf("missing job %s", name)
		}
		var job gitlabJob
		if err := node.Decode(&job); err != nil {
			t.Fatal(err)
		}
		jobs[name] = job
	}
	return jobs
}

func TestGitLabPublishContract(t *testing.T) {
	jobs := gitlabJobs(t)
	for _, gate := range []string{"just check", "just go-check", "just lint", "just docs-check", "just config-check", "just helm-check", "GOFLAGS=-tags=libsqlite3 CGO_CFLAGS=-I/usr/include/sqlcipher just credential-broker-check", "just security", "go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact ."} {
		if !slices.Contains(jobs["test-job"].Script, gate) {
			t.Errorf("required CI gate missing: %s", gate)
		}
	}
	build := jobs["build-image"]
	if !slices.Equal(build.Needs, []string{"test-job"}) || !slices.Equal(jobs["deploy"].Needs, []string{"build-image"}) {
		t.Fatal("publish/deploy can bypass a prerequisite")
	}
	want := map[string]string{"hermes-hub-control": "prod-control", "hermes-hub-runtime": "prod", "cli-tools": "cli-tools", "cell-proxy": "cell-proxy", "credential-broker": "credential-broker"}
	if len(build.Parallel.Matrix) != len(want) {
		t.Fatal("portable image set changed")
	}
	for _, item := range build.Parallel.Matrix {
		name, target := item["HUB_IMAGE_NAME"], item["HUB_BUILD_TARGET"]
		if want[name] != target || target == "" {
			t.Errorf("unexpected image/target %s/%s", name, target)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing portable images: %v", want)
	}
	for _, job := range []gitlabJob{build, jobs["deploy"]} {
		if len(job.Rules) != 1 || !strings.Contains(job.Rules[0].If, "$CI_DEFAULT_BRANCH") || !strings.Contains(job.Rules[0].If, `$CI_COMMIT_REF_PROTECTED == "true"`) || job.ResourceGroup == "" {
			t.Fatal("registry or cluster mutation lacks protected branch guard or serialization")
		}
	}
	deploy := jobs["deploy"]
	if deploy.Rules[0].When != "manual" || !strings.Contains(deploy.Rules[0].If, `$HUB_DEPLOY_ENABLED == "true"`) {
		t.Fatal("deploy must be explicitly enabled and manual")
	}
	script := strings.Join(build.Script, "\n")
	for _, required := range []string{"--password-stdin", "mktemp -d", "trap ", `docker-publish-to "$HUB_REGISTRY_HOST/$HUB_REGISTRY_REPO/$HUB_IMAGE_NAME" "$HUB_BUILD_TARGET" "$HUB_ENV"`} {
		if !strings.Contains(script, required) {
			t.Errorf("publish contract missing %s", required)
		}
	}
}

func TestDockerPublishToRegistry(t *testing.T) {
	t.Setenv("HUB_DOCKER_CACHE_FROM", "")
	t.Setenv("HUB_DOCKER_CACHE_TO", "")
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct{ image, target, environment, revision string }{
		{"localhost:5001/team/hermes-hub-control", "prod-control", "dev", sha},
		{"registry.example.test/team/credential-broker", "credential-broker", "prod-eu", sha},
	} {
		var got []string
		if err := dockerPublishTo(t.Context(), tc.image, tc.target, tc.environment, tc.revision, func(_ context.Context, args ...string) error { got = args; return nil }); err != nil {
			t.Fatal(err)
		}
		want := []string{"build", "--target", tc.target, "-t", tc.image + ":" + sha, "-t", tc.image + ":edge-" + tc.environment, "--push", "--build-arg", "GIT_SHA=" + sha, "-f", "docker/Dockerfile", "."}
		if !slices.Equal(got, want) {
			t.Fatalf("publish command: got %v want %v", got, want)
		}
	}
	for _, tc := range []struct{ image, target, environment, revision string }{
		{"https://user:password@registry.test/team/image", "prod", "dev", sha},
		{"registry.test/team/image:edge-dev", "prod", "dev", sha},
		{"registry.test/team/image", "dev", "dev", sha},
		{"registry.test/team/image", "prod", "../prod", sha},
		{"registry.test/team/image", "prod", "dev", "short"},
	} {
		called := false
		err := dockerPublishTo(t.Context(), tc.image, tc.target, tc.environment, tc.revision, func(context.Context, ...string) error { called = true; return nil })
		if err == nil || called {
			t.Fatal("invalid publish input reached Docker")
		}
	}
	sentinel := errors.New("push failed")
	if err := dockerPublishTo(t.Context(), "localhost:5001/team/image", "prod", "dev", sha, func(context.Context, ...string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("push failure lost: %v", err)
	}
}

func TestGitLabDeployPinsRenderedImages(t *testing.T) {
	// Execute the real job script; only replace the external cluster mutation.
	bash := "bash"
	if runtime.GOOS == "windows" {
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bash = filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe")
	}
	if _, err := exec.LookPath(bash); err != nil {
		t.Fatal("Bash is required to verify the GitLab deploy script")
	}
	dir := t.TempDir()
	stub := "#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$HUB_ARGV_FILE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "argv")
	cmd := exec.Command(bash, "-euc", strings.Join(gitlabJobs(t)["deploy"].Script, "\n"))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HUB_ARGV_FILE="+argv,
		"HUB_REGISTRY_HOST=localhost:5001", "HUB_REGISTRY_REPO=team", "HUB_KUBECONFIG=synthetic", "HUB_KUBE_CONTEXT=contract",
		"HUB_HELM_RELEASE=portable", "HUB_HELM_NAMESPACE=contract", "HUB_HELM_VALUES_FILE=synthetic", "HUB_ENV=dev", "CI_COMMIT_SHA="+strings.Repeat("b", 40))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("deploy script: %v: %s", err, out)
	}
	data, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(data)), "\n")
	var flags []string
	for i, arg := range args {
		if arg == "--set-string" {
			flags = append(flags, arg, args[i+1])
		}
	}
	for _, required := range []string{"upgrade", "--install", "--atomic", "--wait", "--reset-values"} {
		if !slices.Contains(args, required) {
			t.Errorf("deploy flag missing: %s", required)
		}
	}
	// An operator override left over from a prior release must not beat the SHA.
	stale := []string{"--set-string", "services.supervisor.image.digest=sha256:" + strings.Repeat("c", 64)}
	objects := helmRender(t, "portable", append(stale, flags...)...)
	deployments := 0
	for _, object := range objects {
		if object["kind"] != "Deployment" {
			continue
		}
		deployments++
		spec := helmMap(t, object, "spec", "template", "spec")
		container := spec["containers"].([]any)[0].(map[string]any)
		name := container["name"].(string)
		image := "hermes-hub-control"
		if name == "credential-broker" {
			image = name
		}
		if got, want := container["image"], "localhost:5001/team/"+image+":"+strings.Repeat("b", 40); got != want {
			t.Errorf("%s image = %v, want %s", name, got, want)
		}
		if name == "supervisor" && !strings.Contains(strings.Join(anyStrings(container["args"]), " "), "localhost:5001/team/hermes-hub-runtime:"+strings.Repeat("b", 40)) {
			t.Error("supervisor runtime is not pinned to the published registry revision")
		}
	}
	if deployments != 5 {
		t.Fatalf("expected five pinned deployments, got %d", deployments)
	}
}

func anyStrings(value any) []string {
	var result []string
	for _, item := range value.([]any) {
		result = append(result, item.(string))
	}
	return result
}
