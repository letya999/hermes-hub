package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore/gitsource"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// skillGitAdmissionEndpoint is the controller's published loopback address;
// the container env pair overrides it for tests and remote operators.
var skillGitAdmissionEndpoint = "http://127.0.0.1:8545/admit"

// skillGitCell builds the operator's GitOps: the same bounded /cli-exec cell
// the gateway drives. The env pair wins; without it the rendered
// toolhub.auth supplies the token for the loopback endpoint.
func skillGitCell(dir, principal string) (gitsource.ExecCell, error) {
	exec, err := api.CLIExecFromEnv()
	if err != nil {
		return gitsource.ExecCell{}, err
	}
	if exec == nil {
		token, err := skillGitToken(dir)
		if err != nil {
			return gitsource.ExecCell{}, err
		}
		if exec, err = api.CLIExecClient(skillGitAdmissionEndpoint, token); err != nil {
			return gitsource.ExecCell{}, err
		}
	}
	return gitsource.ExecCell{Exec: exec, Principal: principal, ContextID: principal, Token: os.Getenv("GIT_TOKEN")}, nil
}

// skillGitToken reads the controller admission token rendered into the
// infra-owner's toolhub.auth (KEY=value lines, mode 0600). The shared
// workload-controller authenticates only the infra space's key, so a
// secondary space's own token file must not be presented.
func skillGitToken(dir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(stack.InfraSpaceDir(dir), "toolhub.auth")) // #nosec G703 -- operator-supplied deployment dir
	if err != nil {
		return "", fmt.Errorf("skill git: admission token unavailable (is the stack rendered?): %w", err)
	}
	for line := range strings.Lines(string(body)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "HUB_TOOLHIVE_ADMISSION_TOKEN="); ok {
			return v, nil
		}
	}
	return "", fmt.Errorf("skill git: toolhub.auth carries no admission token")
}
