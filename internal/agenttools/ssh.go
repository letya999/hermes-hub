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
	call := func(fn func(context.Context, *sshcap.Service, Input) (any, error)) func(context.Context, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, any, error) {
		return func(ctx context.Context, _ *mcp.CallToolRequest, r Input) (*mcp.CallToolResult, any, error) {
			svc, err := t.sshService()
			if err != nil {
				return nil, nil, err
			}
			out, err := fn(ctx, svc, r)
			return nil, out, err
		}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "ssh_hosts", Description: "List configured SSH host aliases with host/port/user, capabilities and bounds. Host credentials and host keys are never returned."}, call(func(_ context.Context, svc *sshcap.Service, _ Input) (any, error) {
		return map[string]any{"hosts": svc.Hosts()}, nil
	}))
	mcp.AddTool(s, &mcp.Tool{Name: "ssh_exec", Description: "Run one allowlisted diagnostic command on a configured host alias. Write commands require the ssh_write grant. Output, duration and concurrency are bounded."}, call(func(ctx context.Context, svc *sshcap.Service, r Input) (any, error) {
		return svc.Exec(ctx, r.Host, r.Command)
	}))
	mcp.AddTool(s, &mcp.Tool{Name: "ssh_read", Description: "Read one allowlisted remote file (bounded UTF-8 text) from a configured host alias."}, call(func(ctx context.Context, svc *sshcap.Service, r Input) (any, error) {
		return svc.Read(ctx, r.Host, r.Path)
	}))
	if t.SSHGrants.Write {
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_write", Description: "Replace one allowlisted remote file atomically (bounded UTF-8 text). Privileged ssh_write capability."}, call(func(ctx context.Context, svc *sshcap.Service, r Input) (any, error) {
			return svc.Write(ctx, r.Host, r.Path, r.Text)
		}))
	}
	if t.SSHGrants.Shell {
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_shell_open", Description: "Open a bounded interactive PTY shell on a configured host alias. Privileged ssh_shell capability."}, call(func(ctx context.Context, svc *sshcap.Service, r Input) (any, error) {
			return svc.ShellOpen(ctx, r.Host)
		}))
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_shell_send", Description: "Write input (max 4 KiB) to an open SSH shell by id."}, call(func(_ context.Context, svc *sshcap.Service, r Input) (any, error) {
			return map[string]any{"id": r.ID, "sent": len(r.Data)}, svc.ShellSend(r.ID, r.Data)
		}))
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_shell_read", Description: "Read new shell output from the bounded ring buffer at offset."}, call(func(_ context.Context, svc *sshcap.Service, r Input) (any, error) {
			return svc.ShellRead(r.ID, r.Offset)
		}))
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_shell_close", Description: "Close an open SSH shell by id."}, call(func(_ context.Context, svc *sshcap.Service, r Input) (any, error) {
			return map[string]any{"id": r.ID, "closed": true}, svc.ShellClose(r.ID)
		}))
	}
	if t.SSHGrants.Tunnel {
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_tunnel_open", Description: "Open a managed loopback tunnel to a pre-approved remote endpoint on a configured host alias. Privileged ssh_tunnel capability."}, call(func(ctx context.Context, svc *sshcap.Service, r Input) (any, error) {
			return svc.TunnelOpen(ctx, r.Host, r.Name)
		}))
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_tunnel_list", Description: "List open managed SSH tunnels."}, call(func(_ context.Context, svc *sshcap.Service, _ Input) (any, error) {
			return map[string]any{"tunnels": svc.TunnelList()}, nil
		}))
		mcp.AddTool(s, &mcp.Tool{Name: "ssh_tunnel_close", Description: "Close one managed SSH tunnel by id."}, call(func(_ context.Context, svc *sshcap.Service, r Input) (any, error) {
			return map[string]any{"id": r.ID, "closed": true}, svc.TunnelClose(r.ID)
		}))
	}
}
