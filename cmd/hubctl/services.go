package main

import "slices"

// ServiceDescriptor is the process launch contract. Edges name outgoing
// dependencies; process mode wires only edges enabled by the selected profile.
type ServiceDescriptor struct {
	Name, Binary, Zone, ReplicaClass string
	Port                             int
	Env, Edges, Profiles             []string
	DataDir                          string
}

func serviceDescriptors() []ServiceDescriptor {
	core := []string{"minimal", "family", "enterprise"}
	family := []string{"family", "enterprise"}
	enterprise := []string{"enterprise"}
	return []ServiceDescriptor{
		{Name: "spawn-controller", Binary: "spawn-controller", Port: 9201, Zone: "control", ReplicaClass: "single", DataDir: "spawn-controller.db", Profiles: core},
		{Name: "harness-runtime", Binary: "harness-runtime", Port: 9202, Zone: "space", ReplicaClass: "multi", DataDir: "harness-runtime.db", Profiles: core, Edges: []string{"spawn-controller", "identity-hub", "task-engine", "tool-hub", "model-gateway", "egress-gateway"}},
		{Name: "identity-hub", Binary: "identity-hub", Port: 9203, Zone: "control", ReplicaClass: "single", DataDir: "identity-hub.db", Profiles: family},
		{Name: "task-engine", Binary: "task-engine", Port: 9204, Zone: "control", ReplicaClass: "single", DataDir: "task-engine.db", Profiles: family, Edges: []string{"spawn-controller", "identity-hub", "harness-runtime"}},
		{Name: "channel-hub", Binary: "channel-hub", Port: 9205, Zone: "edge", ReplicaClass: "single", DataDir: "channel-hub.db", Profiles: family, Edges: []string{"identity-hub", "task-engine", "model-gateway"}},
		{Name: "tool-hub", Binary: "tool-hub", Port: 9206, Zone: "agent", ReplicaClass: "multi", DataDir: "tool-hub.db", Profiles: family, Edges: []string{"spawn-controller", "identity-hub", "task-engine", "proactive-engine", "memory-hub", "model-gateway", "hosting-hub", "credential-broker", "egress-gateway"}},
		{Name: "model-gateway", Binary: "model-gateway", Port: 9207, Zone: "agent", ReplicaClass: "multi", DataDir: "model-gateway.db", Profiles: family, Edges: []string{"credential-broker", "egress-gateway"}},
		{Name: "credential-broker", Binary: "credential-broker", Port: 9208, Zone: "secret", ReplicaClass: "single", DataDir: "credential-broker.db", Profiles: family},
		{Name: "egress-gateway", Binary: "egress-gateway", Port: 9209, Zone: "egress", ReplicaClass: "multi", DataDir: "egress-gateway.db", Profiles: family, Edges: []string{"credential-broker"}},
		{Name: "proactive-engine", Binary: "proactive-engine", Port: 9210, Zone: "control", ReplicaClass: "single", DataDir: "proactive-engine.db", Profiles: enterprise, Edges: []string{"identity-hub", "task-engine", "memory-hub"}},
		{Name: "memory-hub", Binary: "memory-hub", Port: 9211, Zone: "agent", ReplicaClass: "single", DataDir: "memory-hub.db", Profiles: enterprise},
		{Name: "ingress-gateway", Binary: "ingress-gateway", Port: 9212, Zone: "edge", ReplicaClass: "multi", DataDir: "ingress-gateway.db", Profiles: enterprise, Edges: []string{"channel-hub", "hosting-hub", "identity-hub"}},
		{Name: "hosting-hub", Binary: "hosting-hub", Port: 9213, Zone: "edge", ReplicaClass: "single", DataDir: "hosting-hub.db", Profiles: enterprise, Edges: []string{"egress-gateway", "credential-broker"}},
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
