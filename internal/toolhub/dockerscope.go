package toolhub

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// dockerScope bounds which Docker objects a mounted socket may touch: the
// socket sees every container on the host, so inspect/exec/logs/rm and the
// network/volume equivalents only proceed when the target provably belongs
// to this stack — its compose project label, the hermes-hub.scope label the
// hub stamps at spawn, or attachment to the managed agent network. Foreign
// objects are denied and logged to the toolhub audit stream.
type dockerScope struct {
	project  string // own com.docker.compose.project value
	agentNet string // managed agent network this space owns (optional)
	run      func(context.Context, string, ...string) ([]byte, error)
	mu       sync.Mutex
	owned    map[string]bool
}

var (
	dockerScopeMu sync.RWMutex
	dockerScopeOf *dockerScope
)

// SetDockerScope installs the process-wide boundary for docker argv built by
// the exec/scratch helpers; nil disables enforcement (tests, socketless
// deployments). The controller wires its own scope from the same env pair.
func SetDockerScope(project, agentNet string) {
	dockerScopeMu.Lock()
	defer dockerScopeMu.Unlock()
	if strings.TrimSpace(project) == "" {
		dockerScopeOf = nil
		return
	}
	dockerScopeOf = newDockerScope(project, agentNet, localCommand)
}

func currentDockerScope() *dockerScope {
	dockerScopeMu.RLock()
	defer dockerScopeMu.RUnlock()
	return dockerScopeOf
}

func newDockerScope(project, agentNet string, run func(context.Context, string, ...string) ([]byte, error)) *dockerScope {
	if strings.TrimSpace(project) == "" {
		// Without a project identity every unlabeled object would compare
		// equal to the empty scope — the guard must stay off, not open.
		return nil
	}
	return &dockerScope{project: project, agentNet: agentNet, run: run, owned: map[string]bool{}}
}

// register marks a name this process spawned; checked before label proof so
// containers a spawner cannot label (ToolHive) still pass.
func (s *dockerScope) register(names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		s.owned[kindName{name: name}.key()] = true
	}
}

// owns reports whether the named object belongs to this stack. An object that
// cannot be inspected does not exist, so touching it is a no-op and allowed;
// only a live foreign object is denied.
func (s *dockerScope) owns(ctx context.Context, kind, name string) bool {
	key := kindName{kind: kind, name: name}.key()
	s.mu.Lock()
	if s.owned[key] {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()
	var body []byte
	var err error
	switch kind {
	case "network", "volume":
		body, err = s.run(ctx, "docker", kind, "inspect", "--format", "{{json .Labels}}", name)
	default:
		kind = "container"
		body, err = s.run(ctx, "docker", "inspect", "--format", `{{json .Config.Labels}}|{{json .NetworkSettings.Networks}}`, name)
	}
	if err != nil {
		return true
	}
	if kind == "container" {
		labels, networks := dockerScopeLabels(body)
		if labels["com.docker.compose.project"] == s.project || labels["hermes-hub.scope"] == s.project {
			s.register(name)
			return true
		}
		if len(labels) == 0 && len(networks) == 0 {
			// An inspectable object with neither labels nor networks never
			// matches by accident; only an explicit scope mark may pass.
			log.Printf("docker-scope: denied unmarked container name=%s project=%q", name, s.project)
			return false
		}
		if s.agentNet != "" {
			if _, ok := networks[s.agentNet]; ok {
				s.register(name)
				return true
			}
		}
	} else if dockerScopeLabelMap(body)["hermes-hub.scope"] == s.project {
		s.mu.Lock()
		s.owned[key] = true
		s.mu.Unlock()
		return true
	}
	log.Printf("docker-scope: denied kind=%s name=%s project=%q agent-net=%q", kind, name, s.project, s.agentNet)
	return false
}

// admitSupervisedRuntime opens the executor channel to a supervisor-spawned
// runtime container that lives outside this stack's labels — sibling spaces
// share one toolhub, and their runtimes sit on their own agent networks. The
// caller derives the name deterministically (hermes-context-<hash16> of the
// admitted binding); the supervisor stamps the full hash in hermes-hub.context,
// so the label prefix-match proves the container IS that binding's runtime and
// cannot be satisfied by a foreign one. Proven names join the owned set so
// guardDocker's per-operand checks pass for the same call path.
func (s *dockerScope) admitSupervisedRuntime(ctx context.Context, name string) bool {
	if s.owns(ctx, "container", name) {
		return true
	}
	suffix, ok := strings.CutPrefix(name, "hermes-context-")
	if !ok || len(suffix) != 16 {
		return false
	}
	if _, err := hex.DecodeString(suffix); err != nil {
		return false
	}
	body, err := s.run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "hermes-hub.context"}}|{{index .Config.Labels "hermes-hub.owner"}}`, name)
	if err != nil {
		return false
	}
	contextLabel, owner, _ := strings.Cut(strings.TrimSpace(string(body)), "|")
	if len(contextLabel) != 64 || !strings.HasPrefix(contextLabel, suffix) || owner == "" || owner == "<no value>" {
		log.Printf("docker-scope: denied supervised-runtime proof name=%s project=%q", name, s.project)
		return false
	}
	s.register(name)
	return true
}

type kindName struct{ kind, name string }

func (k kindName) key() string {
	if k.kind == "" {
		k.kind = "container"
	}
	return k.kind + "\x00" + k.name
}

// dockerScopeLabels splits the container inspect format into label and
// network maps; malformed output yields empty maps, which deny.
func dockerScopeLabels(body []byte) (map[string]string, map[string]struct{ IPAddress string }) {
	labels := map[string]string{}
	networks := map[string]struct{ IPAddress string }{}
	parts := strings.SplitN(strings.TrimSpace(string(body)), "|", 2)
	_ = json.Unmarshal([]byte(parts[0]), &labels)
	if len(parts) == 2 {
		_ = json.Unmarshal([]byte(parts[1]), &networks)
	}
	return labels, networks
}

func dockerScopeLabelMap(body []byte) map[string]string {
	labels := map[string]string{}
	raw := strings.TrimSpace(string(body))
	if raw == "null" {
		return labels
	}
	_ = json.Unmarshal([]byte(raw), &labels)
	return labels
}

// dockerFlagsWithValues are the docker CLI flags whose next argv token is a
// value, not an object operand. Only the subset the hub renders needs to be
// exact; an unknown flag is treated as boolean, which errs toward checking
// extra operands — fail closed.
var dockerFlagsWithValues = map[string]bool{
	"-d": true, "--driver": true, "-o": true, "--opt": true, "--label": true,
	"--name": true, "--network": true, "-e": true, "--env": true, "--env-file": true,
	"-u": true, "--user": true, "-w": true, "--workdir": true, "--entrypoint": true,
	"-v": true, "--volume": true, "--mount": true, "-p": true, "--publish": true,
	"-f": true, "--format": true, "--filter": true, "--memory": true, "-m": true,
	"--cpus": true, "--pids-limit": true, "--stop-timeout": true, "--stop-signal": true,
	"--cap-add": true, "--cap-drop": true, "--security-opt": true, "--tmpfs": true,
	"--restart": true, "--hostname": true, "-h": true, "--ip": true, "--log-driver": true,
	"--log-opt": true, "--ulimit": true, "--gpus": true, "--platform": true, "--pull": true,
	"--alias": true, "--ip6": true, "--link": true, "--cidfile": true, "--shm-size": true,
	"--userns": true, "--pid": true, "--ipc": true, "--isolation": true, "--add-host": true,
	"--dns": true, "--dns-search": true, "--expose": true, "--mac-address": true,
	"--health-cmd": true, "--health-interval": true, "--health-timeout": true, "--health-retries": true,
}

// dockerPositionals returns non-flag operands, skipping values of flags that
// take one. `=` forms need no lookahead.
func dockerPositionals(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			if !strings.Contains(a, "=") && dockerFlagsWithValues[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// guardDocker inspects one docker argv (without the leading "docker") and
// denies operands that name a foreign object. Creation verbs are not checked:
// a colliding --name fails in docker itself, and spawned objects are labelled
// hermes-hub.scope so later operations prove ownership.
func (s *dockerScope) guardDocker(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	op := args[0]
	sub := ""
	rest := args[1:]
	if (op == "network" || op == "volume" || op == "container" || op == "image") && len(rest) > 0 {
		sub, op, rest = op, rest[0], rest[1:]
	}
	names := dockerPositionals(rest)
	check := func(kind, name string) error {
		if !s.owns(ctx, kind, name) {
			return fmt.Errorf("%w: docker %s on foreign %s %q denied", ErrIsolation, op, kind, name)
		}
		return nil
	}
	switch {
	case sub == "network" && (op == "rm" || op == "inspect"):
		for _, name := range names {
			if err := check("network", name); err != nil {
				return err
			}
		}
	case sub == "network" && (op == "connect" || op == "disconnect"):
		// Operand order is <network> <container>; the network handle itself
		// (bridge included) is not modified, the container must be ours.
		if len(names) > 1 {
			if err := check("container", names[1]); err != nil {
				return err
			}
		}
	case sub == "volume" && (op == "rm" || op == "inspect"):
		for _, name := range names {
			if err := check("volume", name); err != nil {
				return err
			}
		}
	case sub == "container" && (op == "inspect" || op == "rm" || op == "logs" || op == "exec" || op == "stop" || op == "kill" || op == "start"):
		for _, name := range names {
			if err := check("container", name); err != nil {
				return err
			}
		}
	case sub == "" && op == "inspect":
		for _, name := range names {
			if err := check("container", name); err != nil {
				return err
			}
		}
	case sub == "" && (op == "rm" || op == "start" || op == "stop" || op == "kill" || op == "restart" || op == "logs" || op == "port" || op == "update" || op == "wait"):
		for _, name := range names {
			if err := check("container", name); err != nil {
				return err
			}
		}
	case sub == "" && op == "exec" && len(names) > 0:
		if err := check("container", names[0]); err != nil {
			return err
		}
	case sub == "" && op == "cp" && len(names) == 2:
		for _, operand := range names {
			if i := strings.Index(operand, ":"); i > 0 {
				if err := check("container", operand[:i]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// dockerTool runs one argv-bearing docker-adjacent command after the scope
// check; docker argv goes through guardDocker, other binaries only get the
// operand check on `rm <name>` — ToolHive removes containers by name.
func (s *dockerScope) runScoped(ctx context.Context, command func(context.Context, string, ...string) ([]byte, error), binary string, args ...string) ([]byte, error) {
	if binary == "docker" {
		if err := s.guardDocker(ctx, args); err != nil {
			return nil, err
		}
	} else if len(args) == 2 && args[0] == "rm" {
		if !s.owns(ctx, "container", args[1]) {
			return nil, fmt.Errorf("%w: %s rm on foreign container %q denied", ErrIsolation, binary, args[1])
		}
	}
	return command(ctx, binary, args...)
}
