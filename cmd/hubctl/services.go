package main

import "slices"

// ServiceDescriptor is the process launch contract. Edges name outgoing
// dependencies; process mode wires only edges enabled by the selected profile.
// DataDir is the per-service data directory created under the launcher's data
// root; the service's own database lives at <data>/<name>.db alongside it.
// Env is the operator environment allowlist: only the named variables pass
// through to the child, on top of launcher-computed wiring.
type ServiceDescriptor struct {
	Name, Binary, Zone, ReplicaClass string
	Port                             int
	Env, Edges, Profiles             []string
	DataDir                          string
	// Spawned services are space workloads started per (space, agent) by
	// spawn-controller, not fixed launcher processes.
	Spawned bool
}

func serviceDescriptors() []ServiceDescriptor {
	core := []string{"minimal", "family", "enterprise"}
	family := []string{"family", "enterprise"}
	enterprise := []string{"enterprise"}
	model := []string{"TZ", "HUB_MANAGED_MODEL_URL", "HUB_MANAGED_MODEL_ID"}
	return []ServiceDescriptor{
		{Name: "spawn-controller", Binary: "spawn-controller", Port: 9201, Zone: "control", ReplicaClass: "single", DataDir: "spawn-controller", Env: model, Profiles: core},
		{Name: "harness-runtime", Binary: "harness-runtime", Port: 9202, Zone: "space", ReplicaClass: "multi", DataDir: "harness-runtime", Env: append(slices.Clone(model), "HUB_FEATURES"), Profiles: core, Spawned: true, Edges: []string{"spawn-controller", "identity-hub", "task-engine", "tool-hub", "model-gateway", "egress-gateway"}},
		{Name: "identity-hub", Binary: "identity-hub", Port: 9203, Zone: "control", ReplicaClass: "single", DataDir: "identity-hub", Env: []string{"TZ"}, Profiles: family},
		{Name: "task-engine", Binary: "task-engine", Port: 9204, Zone: "control", ReplicaClass: "single", DataDir: "task-engine", Env: []string{"TZ"}, Profiles: family, Edges: []string{"spawn-controller", "identity-hub", "harness-runtime"}},
		{Name: "channel-hub", Binary: "channel-hub", Port: 9205, Zone: "edge", ReplicaClass: "single", DataDir: "channel-hub", Env: []string{"TZ"}, Profiles: family, Edges: []string{"identity-hub", "task-engine", "model-gateway"}},
		{Name: "tool-hub", Binary: "tool-hub", Port: 9206, Zone: "agent", ReplicaClass: "multi", DataDir: "tool-hub", Env: []string{"TZ", "HUB_FEATURES"}, Profiles: family, Edges: []string{"spawn-controller", "identity-hub", "task-engine", "proactive-engine", "memory-hub", "model-gateway", "hosting-hub", "credential-broker", "egress-gateway"}},
		{Name: "model-gateway", Binary: "model-gateway", Port: 9207, Zone: "agent", ReplicaClass: "multi", DataDir: "model-gateway", Env: model, Profiles: family, Edges: []string{"credential-broker", "egress-gateway"}},
		{Name: "credential-broker", Binary: "credential-broker", Port: 9208, Zone: "secret", ReplicaClass: "single", DataDir: "credential-broker", Env: []string{"TZ"}, Profiles: family},
		{Name: "egress-gateway", Binary: "egress-gateway", Port: 9209, Zone: "egress", ReplicaClass: "multi", DataDir: "egress-gateway", Env: []string{"TZ", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"}, Profiles: family, Edges: []string{"credential-broker"}},
		{Name: "proactive-engine", Binary: "proactive-engine", Port: 9210, Zone: "control", ReplicaClass: "single", DataDir: "proactive-engine", Env: []string{"TZ"}, Profiles: enterprise, Edges: []string{"identity-hub", "task-engine", "memory-hub"}},
		{Name: "memory-hub", Binary: "memory-hub", Port: 9211, Zone: "agent", ReplicaClass: "single", DataDir: "memory-hub", Env: []string{"TZ"}, Profiles: enterprise},
		{Name: "ingress-gateway", Binary: "ingress-gateway", Port: 9212, Zone: "edge", ReplicaClass: "multi", DataDir: "ingress-gateway", Env: []string{"TZ"}, Profiles: enterprise, Edges: []string{"channel-hub", "hosting-hub", "identity-hub"}},
		{Name: "hosting-hub", Binary: "hosting-hub", Port: 9213, Zone: "edge", ReplicaClass: "single", DataDir: "hosting-hub", Env: []string{"TZ"}, Profiles: enterprise, Edges: []string{"egress-gateway", "credential-broker"}},
	}
}

func profileServices(profile string) []ServiceDescriptor {
	var result []ServiceDescriptor
	for _, svc := range serviceDescriptors() {
		if slices.Contains(svc.Profiles, profile) {
			result = append(result, svc)
		}
	}
	return result
}

// launchedServices is the profile's fixed process set: spawned services are
// started on demand by spawn-controller, not by the launcher.
func launchedServices(profile string) []ServiceDescriptor {
	var result []ServiceDescriptor
	for _, svc := range profileServices(profile) {
		if !svc.Spawned {
			result = append(result, svc)
		}
	}
	return result
}
