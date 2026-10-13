package devcheck

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// All former sequential gates must remain reachable; the aggregate status
// must fail when any matrix child is failed, skipped or cancelled.
func TestCIContainerFanoutPreservesEveryGate(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If       string `yaml:"if"`
			Needs    string `yaml:"needs"`
			Strategy struct {
				FailFast bool `yaml:"fail-fast"`
				Matrix   struct{ Target, Gate []string }
			}
			Steps []struct {
				If  string
				Run string
				Env map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	gates := workflow.Jobs["container-gates"]
	if gates.Needs != "image" || gates.Strategy.FailFast || strings.Join(gates.Strategy.Matrix.Target, ",") != "dev,prod" {
		t.Fatal("matrix lost target or dependency", gates)
	}
	if strings.Join(gates.Strategy.Matrix.Gate, ",") != "smoke,native,supervisor,gateway,capabilities,network,managed,scratch" {
		t.Fatal("container contract omitted", gates.Strategy)
	}
	var scripts string
	for _, step := range gates.Steps {
		scripts += step.Run + "\n"
	}
	for _, required := range []string{"docker-smoke", "hermes-native-contract", "supervisor-contract", "gateway-lifecycle", "hermes-capability-contract", "managed-network-canary", "managed-supervisor-canary", "scratch-workload-canary", "TestDockerScratchExecPausedQuarantine", "credential-broker-docker-check", "org.opencontainers.image.revision", "git rev-parse HEAD"} {
		if !strings.Contains(scripts, required) {
			t.Errorf("gate missing: %s", required)
		}
	}
	aggregate := workflow.Jobs["containers"]
	if aggregate.If != "always()" || aggregate.Needs != "container-gates" || len(aggregate.Steps) != 1 || aggregate.Steps[0].Env["RESULT"] != "${{ needs.container-gates.result }}" || aggregate.Steps[0].Run != `test "$RESULT" = success` {
		t.Fatal("aggregate could admit a non-success", aggregate)
	}
	verifiedTTS := false
	for _, step := range workflow.Jobs["image"].Steps {
		if strings.Contains(step.Run, "tts-cache-contract") {
			verifiedTTS = step.If == "matrix.target == 'tts'" && strings.Contains(step.Run, "docker-pull hermes-hub:test-tts tts")
		}
	}
	if !verifiedTTS {
		t.Fatal("dedicated TTS image lost the actual offline model gate")
	}
}

func TestImageRevisionCannotInvalidateDependencyLayers(t *testing.T) {
	data, err := os.ReadFile("../../docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	stage := ""
	late := false
	labels := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FROM ") {
			fields := strings.Fields(line)
			stage = fields[len(fields)-1]
			late = false
		}
		if strings.HasPrefix(line, "ARG GIT_SHA=") {
			late = true
		}
		if strings.HasPrefix(line, "RUN ") && late {
			t.Errorf("%s changes a filesystem layer after revision ARG", stage)
		}
		if strings.HasPrefix(line, "LABEL org.opencontainers.image.revision=") {
			labels[stage] = true
			if !late {
				t.Errorf("%s lost revision argument", stage)
			}
		}
	}
	for _, stage := range []string{"prod", "dev", "prod-control", "dev-control"} {
		if !labels[stage] {
			t.Errorf("%s lost revision label", stage)
		}
	}
	if labels["runtime"] {
		t.Fatal("shared runtime depends on Git SHA")
	}
}

func TestGoModulesSurviveExportedBuildCache(t *testing.T) {
	data, err := os.ReadFile("../../docker/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	manifest := strings.Index(source, "COPY services/credential-broker/go.mod")
	download := strings.Index(source, "RUN go mod download && cd services/credential-broker && go mod download")
	implementation := strings.Index(source, "COPY services/credential-broker ./services/credential-broker")
	if manifest < 0 || download <= manifest || implementation <= download {
		t.Fatal("module download layer depends on implementation or lacks nested module metadata")
	}
	if strings.Contains(source, "id=hermes-go-mod") {
		t.Fatal("ephemeral module mount hides the exported dependency layer")
	}
}
