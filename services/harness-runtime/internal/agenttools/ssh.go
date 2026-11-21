package agenttools

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/sshcap"
)

// openSSH builds the opt-in SSH capability from the rendered environment.
// The feature is present only when the runtime carries HUB_SSH_CONFIG; write,
// interactive shell and managed tunnels are separate grants. A broken config
// keeps the tools registered so the failure is visible instead of silent.
func (t *Tools) openSSH() {
	configPath := os.Getenv("HUB_SSH_CONFIG")
	if configPath == "" {
		return
	}
	broker, err := credentialbroker.FromEnv("HUB_CREDENTIAL_BROKER_RUNTIME_")
	if err != nil {
		t.sshErr = err
		return
	}
	t.SSHGrants = sshcap.Grants{
		Write:  os.Getenv("HUB_SSH_WRITE") == "true",
		Shell:  os.Getenv("HUB_SSH_SHELL") == "true",
		Tunnel: os.Getenv("HUB_SSH_TUNNEL") == "true",
	}
	t.SSH, t.sshErr = sshcap.Open(sshcap.Options{
		ConfigPath: configPath,
		Grants:     t.SSHGrants,
		Auth:       runtimeEnvelope(),
		Broker:     broker,
		AuditPath:  filepath.Join(t.StateDir, "audit", "ssh.jsonl"),
	})
}

func (t *Tools) sshService() (*sshcap.Service, error) {
	if t.SSH != nil {
		return t.SSH, nil
	}
	if t.sshErr != nil {
		return nil, t.sshErr
	}
	return nil, errors.New("ssh capability is not configured on this runtime")
}
