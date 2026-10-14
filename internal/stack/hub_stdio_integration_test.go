//go:build integration

package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The rendered mcp_servers.hub env uses ${VAR} references. This canary lets the
// pinned Hermes loader resolve and filter them itself, then spawns hubctl
// through the real stdio boundary; an unrelated parent key must not reach it.
func TestHubStdioReminderContract(t *testing.T) {
	image := os.Getenv("HUB_CONTRACT_IMAGE")
	if image == "" {
		image = "hermes-hub:test"
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan error, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		decodeErr := json.NewDecoder(r.Body).Decode(&body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/routines" || decodeErr != nil ||
			r.Header.Get("Authorization") != "Bearer synthetic-contract-auth" ||
			r.Header.Get("X-Hub-Principal") != "contract-user" ||
			body["job_kind"] != "reminder" || body["input"] != "REMINDER-7319" {
			requests <- fmt.Errorf("stdio reminder lost routine payload, auth or principal")
			http.Error(w, "contract mismatch", http.StatusBadRequest)
			return
		}
		requests <- nil
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schedule_id":"stdio-contract"}`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	configured := Config(Settings{User: "contract-user", Model: "synthetic", ModelURL: "http://model.invalid/v1", Tools: testTools("workspace")})
	cfgBody, err := yaml.Marshal(configured)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"HUB_COMMUNICATION_CONTROL_URL": "http://host.docker.internal:" + port,
		"HUB_RUNTIME_AUTH":              "synthetic-contract-auth",
		"HUB_PRINCIPAL_ID":              "contract-user", "HUB_USER_ID": "contract-user",
		"HUB_CONTEXT_ID": "contract-context", "HUB_RUNTIME_ID": "contract-runtime", "HUB_POLICY_VERSION": "policy-1",
		"HUB_TOOL_POLICY": "", "HUB_TOOLS_RO": "false", "HUB_ORG_SCOPED": "false", "HUB_ORG_ACTIONS": "false",
		"HUB_FEATURES": "workspace", "HUB_DEFER_RUNTIME_RESTART": "false",
		"HH_TOKEN": "synthetic-hh-token", "HH_USER_AGENT": "contract-canary",
		"HUB_SELF_ENV_KEYS": "", "HUB_PROTECTED_ENV_KEYS": "", "OPENAI_API_KEY": "synthetic-model-key",
	}
	packed, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup := exec.Command("docker", "run", "--rm", "--user", "0", "-v", filepath.ToSlash(dir)+":/fixture", "--entrypoint", "chmod", image, "-R", "a+rwX", "/fixture")
		if output, err := cleanup.CombinedOutput(); err != nil {
			t.Errorf("fixture cleanup permissions: %v: %s", err, output)
		}
	}()
	const script = `import asyncio,json,os,sys
sys.path.insert(0,'/opt/hermes')
os.environ['HERMES_HOME']='/fixture/hermes-home'
os.environ.update(json.loads(os.environ['HUB_CONTRACT_VALUES']))
from tools.mcp_tool_config import _build_safe_env,_load_mcp_config,_resolve_stdio_command
from mcp import ClientSession
from mcp.client.stdio import StdioServerParameters,stdio_client
servers=_load_mcp_config()
assert 'hub' in servers,list(servers)
cfg=servers['hub']
assert cfg['env'].get('HUB_COMMUNICATION_CONTROL_URL','').startswith('http://host.docker.internal:'),cfg['env'].get('HUB_COMMUNICATION_CONTROL_URL')
required=['HUB_COMMUNICATION_CONTROL_URL','HUB_COMMUNICATION_AUTH','HUB_PRINCIPAL_ID','HUB_USER_ID','HUB_CONTEXT_ID','HUB_RUNTIME_ID','HUB_POLICY_VERSION','HUB_TOOL_POLICY','HUB_TOOLS_RO','HUB_ORG_SCOPED','HUB_ORG_ACTIONS','HUB_FEATURES','HUB_DEFER_RUNTIME_RESTART','OPENAI_API_KEY','HUB_HERMES_CONFIG','HH_TOKEN','HH_USER_AGENT','HUB_SELF_ENV_KEYS','HUB_PROTECTED_ENV_KEYS']
unresolved=[k for k in required if '${' in str(cfg['env'].get(k,''))]
assert not unresolved,unresolved
command,safe_env=_resolve_stdio_command(cfg['command'],_build_safe_env(cfg.get('env')))
assert 'UNRELATED_PROVIDER_KEY' not in safe_env
assert safe_env.get('OPENAI_API_KEY')=='synthetic-model-key'
assert safe_env.get('HUB_HERMES_CONFIG')=='/state/hermes/config.yaml'
async def main():
    p=StdioServerParameters(command=command,args=cfg.get('args',[]),env=safe_env,encoding_error_handler='replace')
    async with stdio_client(p) as (read,write):
        async with ClientSession(read,write) as session:
            await session.initialize()
            names={t.name for t in (await session.list_tools()).tools}
            assert 'routine_create' in names,names
            result=await session.call_tool('routine_create',{'id':'stdio-contract','timezone':'UTC','expression':'once:2099-01-01T00:00:00Z','text':'REMINDER-7319','kind':'reminder'})
            assert not result.is_error,result
asyncio.run(main())
`
	for _, sub := range []string{"workspace", "archive", filepath.Join("state", "hermes"), "hermes-home"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, sub), 0777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "state"), 0777); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{filepath.Join("state", "hermes"), "hermes-home"} {
		if err := os.WriteFile(filepath.Join(dir, sub, "config.yaml"), cfgBody, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "canary.py"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--add-host", "host.docker.internal:host-gateway",
		"-v", filepath.ToSlash(dir)+":/fixture",
		"-v", filepath.ToSlash(filepath.Join(dir, "workspace"))+":/workspace",
		"-v", filepath.ToSlash(filepath.Join(dir, "archive"))+":/archive",
		"-v", filepath.ToSlash(filepath.Join(dir, "state"))+":/state",
		"-e", "HUB_CONTRACT_VALUES="+string(packed), "-e", "UNRELATED_PROVIDER_KEY=leak-probe",
		"--entrypoint", "python", image, "/fixture/canary.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("image MCP stdio contract: %v: %s", err, output)
	}
	select {
	case err := <-requests:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("MCP child did not reach routine service")
	}
}
