package stack

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// composePlan is the stack passport's imperative topology (SPEC-0092 R3.2):
// per-space instances render local containers. A space without a published
// revision carries no plan — the legacy settings conditionals keep their
// authority.
type composePlan struct {
	local map[string]string // emitted compose key → catalog kind
}

// serviceKind resolves an instance id to its catalog kind: <kind>-<suffix>
// instances of multi-instance kinds share the kind.
func serviceKind(instance string) string {
	if strings.HasPrefix(instance, "cliproxy-") {
		return "cliproxy"
	}
	return instance
}

// planServiceName maps a per-space instance to its compose key. A bare
// cliproxy instance takes the user suffix so it never collides with the
// shared alias on the runtime network; suffixed instances render under
// their own name.
func planServiceName(instance string, s Settings) string {
	switch instance {
	case "cliproxy", "browser":
		return instance + "-" + s.User
	case "hosting":
		return hostingName(s)
	case "hosting-controller":
		return hostingName(s) + "-ctl"
	}
	return instance
}

func newComposePlan(s Settings) *composePlan {
	if len(s.PassportServices) == 0 {
		return nil
	}
	p := &composePlan{local: map[string]string{}}
	for instance, mode := range s.PassportServices {
		switch mode {
		case "per-space":
			p.local[planServiceName(instance, s)] = serviceKind(instance)
		case "shared":
			// Shared instances are the infra stack's own containers; only
			// the infra space renders them locally.
			if s.RendersInfra() {
				p.local[instance] = serviceKind(instance)
			}
		}
	}
	return p
}

// cliproxyNames lists the emitted names of every local cliproxy instance —
// one container per declared instance.
func (p *composePlan) cliproxyNames() []string {
	if p == nil {
		return nil
	}
	names := []string{}
	for name, kind := range p.local {
		if kind == "cliproxy" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// declared reports whether an emitted service belongs to the plan — either
// directly or as an implementation companion (<base>-init/-ctl) of a
// declared key.
func (p *composePlan) declared(key string) bool {
	if _, ok := p.local[key]; ok {
		return true
	}
	for _, suffix := range []string{"-init", "-ctl"} {
		if base, ok := strings.CutSuffix(key, suffix); ok {
			if _, found := p.local[base]; found {
				return true
			}
		}
	}
	return false
}

// filter drops compose services the passport did not declare locally — the
// published services map bounds the topology; feature flags cannot add to
// it. hermes-runtime keeps the ExecutionMode contract: its presence is mode
// state, and supervised spaces already removed it.
func (p *composePlan) filter(services M) M {
	if p == nil {
		return services
	}
	for key := range services {
		if key == "hermes-runtime" {
			continue
		}
		if p.declared(key) {
			continue
		}
		delete(services, key)
	}
	return services
}

// check fails the render when a declared local instance produced no compose
// service — settings/passport drift the migration must resolve, never a
// silent smaller topology.
func (p *composePlan) check(services M) error {
	if p == nil {
		return nil
	}
	missing := []string{}
	for name := range p.local {
		if _, ok := services[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("stack passport declares services with no rendered container: %s (republish stack.yaml or fix the feature flags that emit them)", strings.Join(missing, ", "))
	}
	return nil
}

// ComposeServiceName resolves a stack instance name to the compose service
// key this space renders — per-space instances carry their emitted name
// (SPEC-0092 A4: a recreate acts on the owning container). "" means the
// target has no compose service here: a supervised hermes-runtime is
// supervisor-spawned, a shared service renders only in the infra project.
func ComposeServiceName(s Settings, instance string) string {
	mode := s.PassportServices[instance]
	if mode == "supervised" || (mode == "shared" && !s.RendersInfra()) {
		return ""
	}
	if mode == "shared" {
		return instance
	}
	return planServiceName(instance, s)
}

// AgentImageRequired reports whether the build set must produce the agent
// image tag explicitly: a supervised hermes-runtime never renders a compose
// service, yet the supervisor spawns it by tag at container start. The
// infra space keeps building the shared tag the same way.
func AgentImageRequired(s Settings, services M) bool {
	if _, rendered := services["hermes-runtime"]; rendered {
		return false
	}
	if s.RendersInfra() {
		return true
	}
	return s.PassportServices["hermes-runtime"] == "supervised"
}

// ComposeChecked renders and verifies the declared topology: every per-space
// instance in the stack passport has a rendered container.
func ComposeChecked(s Settings, projectRoot, dir string) (M, error) {
	result := compose(s, projectRoot, dir, true)
	services, _ := result["services"].(M)
	if err := newComposePlan(s).check(services); err != nil {
		return nil, err
	}
	return result, nil
}

// SplitInfraCompose keeps the infra owner's shared services in the original
// project (and therefore on their existing volumes), while placing its
// per-space services in a separate project. The published stack passport is
// the only source of service placement.
func SplitInfraCompose(s Settings, full M) (shared, user M) {
	if !s.RendersInfra() || !HasPerSpaceServices(s) {
		return full, nil
	}
	shared, user = maps.Clone(full), maps.Clone(full)
	sharedServices, userServices := M{}, M{}
	services := full["services"].(M)
	for name, service := range services {
		perSpace := false
		for instance, mode := range s.PassportServices {
			if mode != "per-space" {
				continue
			}
			base := planServiceName(instance, s)
			if name == base || name == base+"-init" || name == base+"-ctl" {
				perSpace = true
				break
			}
		}
		if perSpace {
			userServices[name] = service
		} else {
			sharedServices[name] = service
		}
	}
	shared["services"], user["services"] = sharedServices, userServices
	user["name"] = full["name"].(string) + "-user"
	// The broker granted this relay's signing key in the original project.
	// Retain that volume name when moving the relay to its own project.
	userVolumes := maps.Clone(full["volumes"].(M))
	if v, ok := userVolumes["broker-secrets-model"].(M); ok {
		copy := maps.Clone(v)
		copy["name"] = full["name"].(string) + "_broker-secrets-model"
		userVolumes["broker-secrets-model"] = copy
	}
	user["volumes"] = userVolumes
	userNetworks := maps.Clone(full["networks"].(M))
	if _, ok := userNetworks[managedAgentNetwork(s)]; ok {
		userNetworks[managedAgentNetwork(s)] = M{"name": managedAgentNetwork(s), "external": true}
	}
	user["networks"] = userNetworks
	return shared, user
}

func HasPerSpaceServices(s Settings) bool {
	for _, mode := range s.PassportServices {
		if mode == "per-space" {
			return true
		}
	}
	return false
}
