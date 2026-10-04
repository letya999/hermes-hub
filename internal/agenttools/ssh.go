package agenttools

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/sshcap"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func (t *Tools) registerSSH(s *mcp.Server) {
	if os.Getenv("HUB_SSH_CONFIG") == "" {
		return
	}
	add := func(name, description string) {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: description}, func(ctx context.Context, _ *mcp.CallToolRequest, r Input) (*mcp.CallToolResult, map[string]any, error) {
			out, err := t.Call(ctx, name, r)
			if err != nil {
				return nil, nil, err
			}
			result, ok := out.(map[string]any)
			if !ok {
				return nil, map[string]any{"result": out}, nil
			}
			return nil, result, nil
		})
	}
	add("ssh_hosts", "List configured SSH host aliases with host/port/user, capabilities and bounds. Host credentials and host keys are never returned.")
	add("ssh_exec", "Run one allowlisted diagnostic command on a configured host alias. Write commands require the ssh_write grant. Output, duration and concurrency are bounded.")
	add("ssh_read", "Read one allowlisted remote file (bounded UTF-8 text) from a configured host alias.")
	if t.SSHGrants.Write {
		add("ssh_write", "Replace one allowlisted remote file atomically (bounded UTF-8 text). Privileged ssh_write capability.")
	}
	if t.SSHGrants.Shell {
		add("ssh_shell_open", "Open a bounded interactive PTY shell on a configured host alias. Privileged ssh_shell capability.")
		add("ssh_shell_send", "Write input (max 4 KiB) to an open SSH shell by id.")
		add("ssh_shell_read", "Read new shell output from the bounded ring buffer at offset.")
		add("ssh_shell_close", "Close an open SSH shell by id.")
	}
	if t.SSHGrants.Tunnel {
		add("ssh_tunnel_open", "Open a managed loopback tunnel to a pre-approved remote endpoint on a configured host alias. Privileged ssh_tunnel capability.")
		add("ssh_tunnel_list", "List open managed SSH tunnels.")
		add("ssh_tunnel_close", "Close one managed SSH tunnel by id.")
	}
}
