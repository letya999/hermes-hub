package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestKubernetesIdentityHelpersAreScopedAndStable(t *testing.T) {
	m, _, _ := kubeManager(t)
	k := m.backend.(*kubernetesBackend)
	rt := Runtime{PrincipalID: "alice", ContextID: "ctx-1", RuntimeMode: "gateway", Generation: "gen-7", Container: "hermes-x", ComputeID: "uid-1"}

	if k.scope(rt) == k.scope(Runtime{PrincipalID: "bob", ContextID: "ctx-1"}) {
		t.Fatal("scope must bind principal identity")
	}
	if !strings.HasPrefix(k.claimName(rt), "space-") || len(k.claimName(rt)) > 40 {
		t.Fatalf("claim name violates bounds: %q", k.claimName(rt))
	}
	if k.secretName(rt) == k.secretName(Runtime{Generation: "gen-8", Container: "hermes-x"}) {
		t.Fatal("secret name must bind the generation")
	}
	annotations := k.annotations(rt, true)
	if annotations[kubeGeneration] != "gen-7" || annotations[kubeOwner] != k.m.ownerID() || annotations[kubeContext] == "" {
		t.Fatalf("generation annotations incomplete: %v", annotations)
	}
	if _, ok := k.annotations(rt, false)[kubeGeneration]; ok {
		t.Fatal("identity-only annotations must not carry the generation")
	}
	labels := k.labels(rt)
	if labels["app.kubernetes.io/component"] != "runtime" || labels[kubeGeneration] != "gen-7" {
		t.Fatalf("labels lost the generation or component: %v", labels)
	}
	for key, value := range labels {
		if len(value) > 63 || len(key) == 0 {
			t.Fatalf("label %q exceeds the Kubernetes budget", key)
		}
	}
	owned := &core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: annotations, UID: "uid-1"}}
	if !k.owned(owned, rt, true) {
		t.Fatal("owned pod rejected")
	}
	if k.owned(&core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{kubeOwner: "foreign"}, UID: "uid-1"}}, rt, true) {
		t.Fatal("foreign pod admitted as owned")
	}
	if k.owned(owned, Runtime{Generation: "", Container: "hermes-x"}, true) {
		t.Fatal("generation-less runtime claimed ownership")
	}
	if k.owned(owned, rt, false) != true || !k.owned(owned, rt, true) {
		t.Fatal("ownership gate drifted")
	}
}

func TestKubernetesPodObservationAndTermination(t *testing.T) {
	ready := core.PodCondition{Type: core.PodReady, Status: core.ConditionTrue, LastTransitionTime: meta.Now()}
	pod := &core.Pod{Status: core.PodStatus{Phase: core.PodRunning, PodIP: "10.0.0.2", Conditions: []core.PodCondition{ready}}}
	obs := podObservation(pod, 8317)
	if obs.State != "running" || obs.Address != "http://10.0.0.2:8317" {
		t.Fatalf("running pod not projected: %+v", obs)
	}
	found := false
	for _, c := range obs.Conditions {
		if c.Name == "pod_ready" {
			found = true
			if !c.OK || c.Reason != ReasonReady {
				t.Fatalf("ready condition not projected: %+v", c)
			}
		}
	}
	if !found || len(obs.Conditions) != 4 {
		t.Fatalf("condition ladder incomplete: %+v", obs.Conditions)
	}
	pod.Status.Phase = core.PodFailed
	if podObservation(pod, 8317).State != "exited" {
		t.Fatal("failed pod must read exited")
	}
	now := meta.NewTime(time.Now())
	pod.DeletionTimestamp = &now
	if podObservation(pod, 8317).State != "stopping" {
		t.Fatal("deletion timestamp must dominate phase")
	}
	if !podTerminationConfirmed(pod) {
		t.Fatal("unscheduled pod can never have started a process")
	}
	pod.Spec.NodeName = "test-node"
	pod.Spec.Containers = []core.Container{{Name: "x"}}
	finished := meta.NewTime(time.Now())
	pod.Status.ContainerStatuses = []core.ContainerStatus{{Name: "x", State: core.ContainerState{Terminated: &core.ContainerStateTerminated{FinishedAt: finished, Reason: "Completed"}}}}
	if !podTerminationConfirmed(pod) {
		t.Fatal("all-terminated pod not confirmed")
	}
	pod.Status.ContainerStatuses[0].State = core.ContainerState{Running: &core.ContainerStateRunning{}}
	if podTerminationConfirmed(pod) {
		t.Fatal("live container slipped through termination confirmation")
	}
	pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{FinishedAt: finished, Reason: "NodeLost"}}
	if podTerminationConfirmed(pod) {
		t.Fatal("NodeLost termination is not reliable evidence")
	}
}

func TestKubernetesIsolationClassAndPeer(t *testing.T) {
	m, _, _ := kubeManager(t)
	k := m.backend.(*kubernetesBackend)
	other := "runc"
	for pod, want := range map[*core.Pod]bool{
		{Spec: core.PodSpec{RuntimeClassName: &other}}:                  false,
		{Spec: core.PodSpec{}}:                                          false,
		{Spec: core.PodSpec{RuntimeClassName: &k.cfg.RuntimeClassName}}: true,
	} {
		if k.podIsolationMatches(pod) != want {
			t.Fatalf("isolation class match drifted: want %v", want)
		}
	}
	peer := k.peer("supervisor")
	if peer.NamespaceSelector == nil || peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != k.cfg.ControlNamespace ||
		peer.PodSelector == nil || peer.PodSelector.MatchLabels["app.kubernetes.io/component"] != "supervisor" {
		t.Fatalf("peer does not select the control plane: %+v", peer)
	}
	port := kubePort(8317, core.ProtocolTCP)
	if port.Port == nil || port.Port.IntValue() != 8317 || port.Protocol == nil || *port.Protocol != core.ProtocolTCP {
		t.Fatalf("network policy port malformed: %+v", port)
	}
	if deleteUID("abc").Preconditions == nil || *deleteUID("abc").Preconditions.UID != "abc" {
		t.Fatal("UID precondition lost")
	}
	policy := k.policy(Runtime{PrincipalID: "a", ContextID: "c", Generation: "g", Container: "pod-x"})
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Egress) != 2 {
		t.Fatalf("network policy must keep supervisor ingress and dns egress only: %+v", policy.Spec)
	}
}

func TestRelayFileAndEnvironmentBounds(t *testing.T) {
	root := t.TempDir()
	if values, err := relayFile(root, "missing.env", false); err != nil || len(values) != 0 {
		t.Fatalf("optional missing relay file must be empty: %v %v", values, err)
	}
	if _, err := relayFile(root, "missing.env", true); err == nil {
		t.Fatal("required missing relay file must fail")
	}
	if err := os.MkdirAll(filepath.Join(root, "subdir.env"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := relayFile(root, "subdir.env", false); err == nil {
		t.Fatal("non-regular relay file must fail")
	}
	if err := os.WriteFile(filepath.Join(root, "relay.env"), []byte("A=1\nB=two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := relayFile(root, "relay.env", true)
	if err != nil || values["A"] != "1" || values["B"] != "two" {
		t.Fatalf("relay env not parsed: %v %v", values, err)
	}
	secret := &core.Secret{ObjectMeta: meta.ObjectMeta{Name: "s"}, Data: map[string][]byte{}}
	env, err := relayEnvironment(secret, map[string]string{"B": "2", "A": "1"}, "r")
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env[0].Name != "A" || env[1].Name != "B" || secret.Data["r0000"] == nil {
		t.Fatalf("relay env not sorted/projected: %+v %+v", env, secret.Data)
	}
	if _, err := relayEnvironment(secret, map[string]string{"BAD NAME": "x"}, "r"); err == nil {
		t.Fatal("invalid env name must fail")
	}
	if _, err := relayEnvironment(secret, map[string]string{"OK": "a\x00b"}, "r"); err == nil {
		t.Fatal("NUL byte in env value must fail")
	}
	oversize := map[string]string{"K": strings.Repeat("x", kubeInputBudget)}
	if _, err := relayEnvironment(secret, oversize, "r"); err == nil {
		t.Fatal("oversized relay input must fail")
	}
}

func TestKubernetesBackendConstructorFailClosed(t *testing.T) {
	m, _, _ := kubeManager(t)
	m.cfg.Kubernetes = KubernetesConfig{Namespace: "runtime-prod"}
	if _, err := newKubernetesBackend(m); err == nil {
		t.Fatal("backend construction without client or kubeconfig must fail")
	}
}

func TestKubernetesPlanRejectsNonManagedRuntime(t *testing.T) {
	m, _, _ := kubeManager(t)
	k := m.backend.(*kubernetesBackend)
	if _, _, err := k.relayPlan(SpawnRequest{Runtime: Runtime{Managed: false}}, &core.Pod{}, &core.Secret{}); err == nil {
		t.Fatal("unmanaged runtime must not receive a relay plan")
	}
}

func TestKubernetesInventoryAndRuntimeLabels(t *testing.T) {
	m, _, client := kubeManager(t)
	k := m.backend.(*kubernetesBackend)
	ctx := context.Background()
	owner := m.ownerID()
	context64 := strings.Repeat("a", 64)

	mkPod := func(name, role, generation string) *core.Pod {
		return &core.Pod{ObjectMeta: meta.ObjectMeta{
			Name:      name,
			Namespace: m.cfg.Kubernetes.Namespace,
			UID:       types.UID("uid-" + name),
			Labels:    map[string]string{kubeOwner: owner[:32]},
			Annotations: map[string]string{
				kubeOwner:      owner,
				kubeContext:    context64,
				kubeGeneration: generation,
				kubeRole:       role,
			},
		}}
	}
	for _, pod := range []*core.Pod{
		mkPod("rt-a", "runtime", "gen-1"),
		mkPod("relay-a", "relay", "gen-1"),
		{ObjectMeta: meta.ObjectMeta{Name: "foreign", Namespace: m.cfg.Kubernetes.Namespace, Labels: map[string]string{}}},
	} {
		if _, err := client.CoreV1().Pods(m.cfg.Kubernetes.Namespace).Create(ctx, pod, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	found, err := k.inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Name != "rt-a" || found[0].ContextKey != context64 || found[0].Generation != "gen-1" {
		t.Fatalf("inventory must return owned runtimes only: %+v", found)
	}

	labels, err := k.runtimeLabels(ctx, "rt-a")
	if err != nil {
		t.Fatal(err)
	}
	if labels["hermes-hub.owner"] != owner || labels["hermes-hub.context"] != context64 || labels["hermes-hub.generation"] != "gen-1" {
		t.Fatalf("runtime labels lost identity: %v", labels)
	}
	if _, err := k.runtimeLabels(ctx, "missing"); !errors.Is(err, ErrRuntimeMissing) {
		t.Fatalf("missing runtime must surface ErrRuntimeMissing, got %v", err)
	}

	bad := mkPod("rt-bad", "runtime", "gen-2")
	bad.Annotations[kubeContext] = "short"
	if _, err := client.CoreV1().Pods(m.cfg.Kubernetes.Namespace).Create(ctx, bad, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.inventory(ctx); err == nil {
		t.Fatal("invalid ownership evidence must fail closed")
	}
}

func TestKubernetesSecretMatches(t *testing.T) {
	m, _, _ := kubeManager(t)
	k := m.backend.(*kubernetesBackend)
	rt := Runtime{PrincipalID: "alice", ContextID: "ctx-1", RuntimeMode: "gateway", Generation: "gen-7", ComputeID: "uid-9"}
	annotations := k.annotations(rt, true)
	immutable := true
	mkSecret := func(data map[string][]byte) *core.Secret {
		return &core.Secret{ObjectMeta: meta.ObjectMeta{Annotations: annotations, UID: "uid-9"}, Immutable: &immutable, Data: data}
	}
	if !k.secretMatches(mkSecret(map[string][]byte{"a": {1}}), mkSecret(map[string][]byte{"a": {1}}), rt) {
		t.Fatal("identical owned immutable secrets must match")
	}
	if k.secretMatches(mkSecret(map[string][]byte{"a": {1}}), mkSecret(map[string][]byte{"a": {2}}), rt) {
		t.Fatal("drifted secret data must not match")
	}
	mutable := false
	mut := mkSecret(map[string][]byte{"a": {1}})
	mut.Immutable = &mutable
	if k.secretMatches(mut, mkSecret(map[string][]byte{"a": {1}}), rt) {
		t.Fatal("mutable secret must not count as current")
	}
	if k.secretMatches(nil, mkSecret(map[string][]byte{"a": {1}}), rt) {
		t.Fatal("missing secret must not match")
	}
	foreign := mkSecret(map[string][]byte{"a": {1}})
	foreign.Annotations = map[string]string{kubeOwner: "foreign-owner"}
	if k.secretMatches(foreign, mkSecret(map[string][]byte{"a": {1}}), rt) {
		t.Fatal("foreign secret must not match")
	}
}
