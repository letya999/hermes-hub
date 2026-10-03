//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"gopkg.in/yaml.v3"
)

// ManagedNetworkCanary checks Docker's private-network and nested read-only
// extension-mount boundaries. It does not start a managed Hermes runtime or
// prove complete egress policy.
func ManagedNetworkCanary(ctx context.Context, image string) (result error) {
	if strings.TrimSpace(image) == "" {
		return errors.New("network canary image is required")
	}
	id := make([]byte, 5)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	suffix := hex.EncodeToString(id)
	agentNet, controlNet, siblingNet := "hermes-cap-agent-"+suffix, "hermes-cap-control-"+suffix, "hermes-cap-sibling-"+suffix
	tool, broker, sibling := "hermes-cap-tool-"+suffix, "hermes-cap-broker-"+suffix, "hermes-cap-peer-"+suffix
	model, relay := "hermes-cap-model-"+suffix, "hermes-cap-relay-"+suffix
	networks, containers := []string{}, []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i := len(containers) - 1; i >= 0; i-- {
			if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", containers[i]).CombinedOutput(); err != nil {
				result = errors.Join(result, fmt.Errorf("remove canary container %s: %w: %s", containers[i], err, out))
			}
		}
		for i := len(networks) - 1; i >= 0; i-- {
			if out, err := exec.CommandContext(cleanup, "docker", "network", "rm", networks[i]).CombinedOutput(); err != nil {
				result = errors.Join(result, fmt.Errorf("remove canary network %s: %w: %s", networks[i], err, out))
			}
		}
	}()
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker %s: %w: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	for _, network := range []struct {
		name     string
		internal bool
	}{{agentNet, true}, {controlNet, false}, {siblingNet, true}} {
		args := []string{"network", "create"}
		if network.internal {
			args = append(args, "--internal")
		}
		if _, err := docker(append(args, network.name)...); err != nil {
			return err
		}
		networks = append(networks, network.name)
	}
	start := func(name, network, alias, port string) error {
		if _, err := docker("run", "-d", "--name", name, "--network", network, "--network-alias", alias,
			"--entrypoint", "python", image, "-m", "http.server", port, "--bind", "0.0.0.0"); err != nil {
			return err
		}
		containers = append(containers, name)
		return nil
	}
	const toolFixture = `from http.server import BaseHTTPRequestHandler,HTTPServer
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200); self.end_headers(); self.wfile.write(b'mcp-ok' if self.path=='/mcp' else b'control-route')
 def do_POST(self): self.do_GET()
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8090),Handler).serve_forever()`
	if _, err := docker("run", "-d", "--name", tool, "--network", controlNet, "--network-alias", "toolhub-control", "--entrypoint", "python", image, "-c", toolFixture); err != nil {
		return err
	}
	containers = append(containers, tool)
	toolRelay := "hermes-cap-tool-relay-" + suffix
	if _, err := docker("run", "-d", "--name", toolRelay, "--network", agentNet, "--network-alias", "toolhub", "--entrypoint", "hub-runtime", image, "toolhub-relay"); err != nil {
		return err
	}
	containers = append(containers, toolRelay)
	if _, err := docker("network", "connect", controlNet, toolRelay); err != nil {
		return err
	}
	if err := start(broker, controlNet, "broker", "8787"); err != nil {
		return err
	}
	if err := start(sibling, siblingNet, "sibling", "8787"); err != nil {
		return err
	}
	const modelFixture = `from http.server import BaseHTTPRequestHandler,HTTPServer
class Handler(BaseHTTPRequestHandler):
 def do_POST(self):
  if self.headers.get('X-Canary-Redirect')=='metadata':
   self.send_response(307); self.send_header('Location','http://169.254.169.254/latest/meta-data/'); self.end_headers(); return
  body=self.path.encode(); self.send_response(200); self.end_headers(); self.wfile.write(body)
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8317),Handler).serve_forever()`
	if _, err := docker("run", "-d", "--name", model, "--network", controlNet, "--network-alias", "cliproxy", "--entrypoint", "python", image, "-c", modelFixture); err != nil {
		return err
	}
	containers = append(containers, model)
	if _, err := docker("run", "-d", "--name", relay, "--network", agentNet, "--network-alias", "model-relay", "-e", "HUB_MODEL_RELAY_MODEL=synthetic", "--entrypoint", "hub-runtime", image, "model-relay"); err != nil {
		return err
	}
	containers = append(containers, relay)
	if _, err := docker("network", "connect", controlNet, relay); err != nil {
		return err
	}
	ip := func(container string) (string, error) {
		return docker("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", container)
	}
	brokerIP, err := ip(broker)
	if err != nil {
		return err
	}
	siblingIP, err := ip(sibling)
	if err != nil {
		return err
	}
	modelIP, err := ip(model)
	if err != nil {
		return err
	}
	toolIP, err := ip(tool)
	if err != nil {
		return err
	}
	const probe = `import os,socket,time,urllib.request
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
with open('/proc/net/route') as routes:
 assert all(fields[1]!='00000000' for line in list(routes)[1:] if (fields:=line.split())), 'agent has an IPv4 default route'
with open('/proc/net/ipv6_route') as routes:
 assert all(not (fields[0]=='0'*32 and fields[1]=='00' and fields[-1]!='lo') for line in routes if (fields:=line.split())), 'agent has an IPv6 default route'
for attempt in range(30):
 try:
  response=opener.open("http://toolhub:8090/mcp",timeout=2)
  assert response.status==200 and response.read()==b'mcp-ok'
  break
 except OSError:
  time.sleep(.2)
else: raise AssertionError("ToolHub unavailable on private network")
try: opener.open("http://toolhub:8090/credentials/private",timeout=2)
except urllib.error.HTTPError as error: assert error.code==404
else: raise AssertionError("ToolHub control route exposed to agent network")
for attempt in range(30):
 try:
  response=opener.open(urllib.request.Request("http://model-relay:8318/v1/chat/completions",data=b'{"model":"synthetic"}',headers={"Content-Type":"application/json"},method="POST"),timeout=2)
  assert response.status==200 and response.read()==b'/v1/chat/completions'
  break
 except OSError:
  time.sleep(.2)
else: raise AssertionError("model relay unavailable on private network")
try: opener.open(urllib.request.Request("http://model-relay:8318/v1/chat/completions",data=b'{"model":"synthetic"}',headers={"Content-Type":"application/json","X-Canary-Redirect":"metadata"},method="POST"),timeout=2)
except urllib.error.HTTPError as error: assert error.code==502 and not error.headers.get("Location")
else: raise AssertionError("model relay forwarded metadata redirect")
for body in (b'{"model":"other"}',b'{"model":"synthetic","model":"synthetic"}'):
 try: opener.open(urllib.request.Request("http://model-relay:8318/v1/chat/completions",data=body,headers={"Content-Type":"application/json"},method="POST"),timeout=2)
 except urllib.error.HTTPError as error: assert error.code==403
 else: raise AssertionError("model relay admitted unreviewed model")
for path in ("/v0/management/api-keys","/v8/management/config","/v1/models"):
 try: opener.open(urllib.request.Request("http://model-relay:8318"+path,data=b'{}',method="POST"),timeout=2)
 except urllib.error.HTTPError as error: assert error.code==404
 else: raise AssertionError("model relay forwarded unreviewed path "+path)
for host in ("broker","sibling","cliproxy","toolhub-control"):
 try: socket.getaddrinfo(host,8787)
 except socket.gaierror: pass
 else: raise AssertionError("private network resolved "+host)
for key,port in (("BROKER_IP",8787),("SIBLING_IP",8787),("MODEL_IP",8317),("TOOL_IP",8090)):
 try: socket.create_connection((os.environ[key],port),timeout=1)
 except OSError: pass
 else: raise AssertionError("private network reached "+key)
print("managed network canary passed: no IPv4/IPv6 default route; narrow ToolHub/model relays reachable; control services and sibling isolated")`
	out, err := docker("run", "--rm", "--network", agentNet, "--entrypoint", "python", "-e", "BROKER_IP="+brokerIP,
		"-e", "SIBLING_IP="+siblingIP, "-e", "MODEL_IP="+modelIP, "-e", "TOOL_IP="+toolIP, image, "-c", probe)
	if err != nil {
		return err
	}
	if err := managedExtensionMountCanary(ctx, image); err != nil {
		return err
	}
	if err := managedConfigPreflightCanary(ctx, image); err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func managedConfigPreflightCanary(ctx context.Context, image string) (result error) {
	dir, err := os.MkdirTemp("", "hermes-cap-config-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			result = errors.Join(result, fmt.Errorf("remove config canary root: %w", err))
		}
	}()
	s := stack.Settings{Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", CapabilityMode: "managed"}
	source, effective := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "config.yaml")
	body, err := yaml.Marshal(stack.Config(s))
	if err != nil {
		return err
	}
	if err := os.WriteFile(source, body, 0600); err != nil {
		return err
	}
	if err := stack.MaterializeHermesConfig(source, effective, stack.MaterializeOptions{
		Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", RuntimeAuthPresent: true, ToolHubReconnect: true,
	}); err != nil {
		return err
	}
	// Linux docker enforces bind-mount permissions: MkdirTemp/0600 files are
	// unreadable to the container uid. The mount is readonly, so writable
	// checks still fail closed regardless of these modes.
	if err := os.Chmod(dir, 0755); err != nil {
		return err
	}
	if err := os.Chmod(effective, 0644); err != nil {
		return err
	}
	for _, name := range []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			return err
		}
	}
	probe := func(expected string, overrides ...string) error {
		args := []string{"run", "--rm", "--network", "none", "--read-only", "--mount", "type=bind,src=" + filepath.ToSlash(dir) + ",dst=/config,readonly",
			"-e", "HERMES_HOME=/config", "-e", "HUB_CAPABILITY_MODE=managed", "-e", "HUB_MANAGED_MODEL_ID=synthetic",
			"-e", "HERMES_BUNDLES_DIR=/config/skill-bundles", "-e", "HERMES_ENABLE_PROJECT_PLUGINS=0",
			"-e", "HUB_MANAGED_MODEL_URL=http://model-relay:8318/v1", "-e", "TZ=UTC", "-e", "HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp",
			"-e", "HUB_RUNTIME_AUTH=synthetic-auth"}
		for _, override := range overrides {
			args = append(args, "-e", override)
		}
		args = append(args, "--entrypoint", "hub-runtime", image, "serve")
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), expected) {
			return fmt.Errorf("managed config preflight expected %q: %v: %s", expected, err, out)
		}
		return nil
	}
	if err := probe("managed capability profile is unset"); err != nil {
		return err
	}
	// With the reviewed environment present the verifier walks past identity
	// and mount checks and fails at the unreachable agent-network relays.
	if err := probe("managed relay toolhub unreachable", "HUB_CAPABILITY_PROFILE_ID=alice-default", "HUB_CAPABILITY_GENERATION=1", "HUB_CAPABILITY_ENVIRONMENT=prod"); err != nil {
		return err
	}
	for _, override := range []string{"HERMES_BUNDLES_DIR=/tmp", "HERMES_ENABLE_PROJECT_PLUGINS=1"} {
		if err := probe("redirected extension discovery", override); err != nil {
			return err
		}
	}
	if err := os.WriteFile(effective, []byte("invalid: ["), 0644); err != nil {
		return err
	}
	if err := probe("managed runtime config preflight"); err != nil {
		return err
	}
	fmt.Println("managed config preflight canary passed: reviewed config reaches isolation guard; malformed config and redirected extension discovery denied before Hermes")
	return nil
}

// removeCanaryTree deletes a canary temp tree that may contain files created
// by container processes. On Linux the host user cannot unlink files owned by
// the container uid, so removal falls back to a throwaway root container
// (CAP_DAC_OVERRIDE clears any ownership/mode).
func removeCanaryTree(ctx context.Context, image, dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	rm := exec.CommandContext(ctx, "docker", "run", "--rm", "--user", "0:0", "-v", filepath.ToSlash(dir)+":/cleanup", "--entrypoint", "sh", image, "-c", "rm -rf /cleanup/* /cleanup/.[!.]*")
	out, rmErr := rm.CombinedOutput()
	if rmErr != nil {
		return errors.Join(err, fmt.Errorf("container cleanup: %w: %s", rmErr, out))
	}
	if retry := os.RemoveAll(dir); retry != nil {
		return errors.Join(err, retry)
	}
	return nil
}

func managedExtensionMountCanary(ctx context.Context, image string) (result error) {
	root, err := os.MkdirTemp("", "hermes-cap-ext-")
	if err != nil {
		return err
	}
	stateRoot := filepath.Join(root, "hermes")
	defer func() {
		if err := removeCanaryTree(ctx, image, root); err != nil {
			result = errors.Join(result, fmt.Errorf("remove extension canary root: %w", err))
		}
	}()
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		return err
	}
	// Linux docker enforces bind-mount permissions: --cap-drop ALL strips
	// CAP_DAC_OVERRIDE so a 0700 foreign-owned dir rejects container writes.
	// Docker Desktop virtualizes ownership so 0700 silently works there.
	if err := os.Chmod(stateRoot, 0777); err != nil {
		return err
	}
	config := filepath.Join(root, "approved-config.yaml")
	placeholder := filepath.Join(stateRoot, "config.yaml")
	for _, file := range []string{config, placeholder} {
		// 0644: the container uid must read the approved config bind mount.
		if err := os.WriteFile(file, []byte("{}\n"), 0644); err != nil {
			return err
		}
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--mount", "type=bind,src=" + filepath.ToSlash(stateRoot) + ",dst=/state/hermes",
		"--mount", "type=bind,src=" + filepath.ToSlash(config) + ",dst=/state/hermes/config.yaml,readonly"}
	for _, name := range []string{"skills", "hooks", "plugins", "skill-bundles", "scripts", "bin", "node", "lsp"} {
		args = append(args, "--tmpfs", "/state/hermes/"+name+":ro,mode=0555")
	}
	const probe = `import os,subprocess,sys
root='/state/hermes'
def write(path):
 with open(path,'w') as file: file.write('injected')
def denied(label,fn):
 try: fn()
 except OSError: return
 raise AssertionError(label+' was writable')
write(root+'/mutable-marker')
os.unlink(root+'/mutable-marker')
for name in ('skills','hooks','plugins','skill-bundles','scripts','bin','node','lsp'):
 path=root+'/'+name
 assert os.path.isdir(path)
 assert os.listdir(path)==[] and os.statvfs(path).f_flag & os.ST_RDONLY
 denied(name+' file',lambda: write(path+'/injected.py'))
 denied(name+' directory',lambda: os.mkdir(path+'/injected'))
 denied(name+' symlink',lambda: os.symlink('..',path+'/escape'))
 denied(name+' replacement',lambda: os.rename(path,path+'-moved'))
denied('config write',lambda: write(root+'/config.yaml'))
denied('config replacement',lambda: os.rename(root+'/config.yaml',root+'/config-moved.yaml'))
probe=root+'/unapproved-hook'
os.mkdir(probe)
with open(probe+'/HOOK.yaml','w') as file: file.write('name: unapproved-hook\nevents: [gateway:startup]\n')
with open(probe+'/handler.py','w') as file: file.write("from pathlib import Path\nPath('/tmp/imported-hook').write_text('bad')\ndef handle(event_type, context): pass\n")
loader='from gateway.hooks import HOOKS_DIR,HookRegistry; from agent.skill_bundles import _bundles_dir,get_skill_bundles; from gateway.platforms.webhook_filters import _resolve_script_path; assert str(HOOKS_DIR)=="/state/hermes/hooks"; assert str(_bundles_dir())=="/state/hermes/skill-bundles"; r=HookRegistry(); r.discover_and_load(); assert r.loaded_hooks==[]; assert get_skill_bundles()=={}; path,error=_resolve_script_path("injected.py"); assert path is None and error'
for _ in range(2):
 subprocess.run([sys.executable,'-c',loader],cwd='/opt/hermes',check=True)
 assert not os.path.exists('/tmp/imported-hook')
import shutil; shutil.rmtree(probe)
print('managed extension mount canary passed: writable state, immutable config, empty read-only extension roots and no hook/bundle/script activation across process restart')`
	args = append(args, "--env", "HERMES_HOME=/state/hermes", "--workdir", "/opt/hermes", "--entrypoint", "/opt/hermes/.venv/bin/python", image, "-c", probe)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("managed extension mount canary: %w: %s", err, out)
	}
	fmt.Print(string(out))
	return nil
}
