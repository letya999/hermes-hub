package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktest "k8s.io/client-go/testing"
)

// Only the API and authenticated Hermes probe are replaced here. These tests
// exercise the real manager/driver contract, not Kubernetes or Hermes acceptance.
func kubeManager(t *testing.T) (*Manager, string, *fake.Clientset) {
	t.Helper()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) {
		t.Error("Kubernetes reached Docker")
		return nil, fmt.Errorf("Docker forbidden")
	}, func(context.Context, string, string) error { return nil })
	client := fake.NewSimpleClientset()
	var sequence atomic.Int64
	client.PrependReactor("create", "*", func(action ktest.Action) (bool, runtime.Object, error) {
		object := action.(ktest.CreateAction).GetObject()
		accessor, err := apiMeta.Accessor(object)
		if err != nil {
			t.Fatal(err)
		}
		accessor.SetUID(types.UID(fmt.Sprintf("uid-%d", sequence.Add(1))))
		if pod, ok := object.(*core.Pod); ok {
			pod.Spec.NodeName = "test-node"
			pod.Status = core.PodStatus{Phase: core.PodRunning, PodIP: "10.244.0.12", Conditions: []core.PodCondition{{Type: core.PodScheduled, Status: core.ConditionTrue}, {Type: core.PodInitialized, Status: core.ConditionTrue}, {Type: core.ContainersReady, Status: core.ConditionTrue}, {Type: core.PodReady, Status: core.ConditionTrue}}}
		}
		return false, nil, nil
	})
	// Simulate kubelet termination and API finalizer collection at the boundary.
	client.PrependReactor("delete", "pods", func(action ktest.Action) (bool, runtime.Object, error) {
		deletion := action.(ktest.DeleteAction)
		object, err := client.Tracker().Get(core.SchemeGroupVersion.WithResource("pods"), action.GetNamespace(), deletion.GetName())
		if err != nil {
			return true, nil, err
		}
		pod := object.(*core.Pod).DeepCopy()
		options := deletion.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != pod.UID {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, pod.Name, fmt.Errorf("UID fence required"))
		}
		pod.DeletionTimestamp = &meta.Time{Time: time.Now()}
		pod.Status.Phase = core.PodSucceeded
		ended := func(containers []core.Container) []core.ContainerStatus {
			var statuses []core.ContainerStatus
			for _, container := range containers {
				statuses = append(statuses, core.ContainerStatus{Name: container.Name, State: core.ContainerState{Terminated: &core.ContainerStateTerminated{FinishedAt: meta.Now(), Reason: "Completed"}}})
			}
			return statuses
		}
		pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses = ended(pod.Spec.Containers), ended(pod.Spec.InitContainers)
		return true, nil, client.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, action.GetNamespace())
	})
	client.PrependReactor("update", "pods", func(action ktest.Action) (bool, runtime.Object, error) {
		pod := action.(ktest.UpdateAction).GetObject().(*core.Pod)
		if action.GetSubresource() == "" && pod.DeletionTimestamp != nil && !slices.Contains(pod.Finalizers, kubeComputeFence) {
			return true, pod, client.Tracker().Delete(core.SchemeGroupVersion.WithResource("pods"), pod.Namespace, pod.Name)
		}
		return false, nil, nil
	})
	m.cfg.ExecutionBackend = "kubernetes"
	m.cfg.Kubernetes = KubernetesConfig{Namespace: "runtime-prod", Client: client, RuntimeClassName: "sandbox-test"}
	m.cfg.Stats = nil
	var err error
	m, err = New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m, root, client
}

func TestKubernetesUnknownTerminationPreservesGenerationAndCapacity(t *testing.T) {
	m, root, client := kubeManager(t)
	first, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	key := runtimeKey(binding(root))
	m.mu.Lock()
	m.items[key].State = Degraded
	m.mu.Unlock()
	client.PrependReactor("delete", "pods", func(action ktest.Action) (bool, runtime.Object, error) {
		name := action.(ktest.DeleteAction).GetName()
		object, err := client.Tracker().Get(core.SchemeGroupVersion.WithResource("pods"), "runtime-prod", name)
		if err != nil {
			return true, nil, err
		}
		pod := object.(*core.Pod).DeepCopy()
		pod.DeletionTimestamp = &meta.Time{Time: time.Now()}
		pod.Status.Phase, pod.Status.Reason = core.PodFailed, "NodeLost"
		return true, nil, client.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, "runtime-prod")
	})
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if _, err := m.Ensure(ctx, binding(root)); err == nil {
		t.Fatal("unknown process death permitted replacement")
	}
	m.mu.Lock()
	current, slots := m.items[key].Runtime, len(m.slots)
	m.mu.Unlock()
	if current.Generation != first.Generation || slots != 1 || kubeCreates(client, "pods") != 2 {
		t.Fatal("unknown process death released generation or capacity")
	}
	pod, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), first.Container, meta.GetOptions{})
	if err != nil || !slices.Contains(pod.Finalizers, kubeComputeFence) {
		t.Fatal("node-loss fence released")
	}
	secrets, err := client.CoreV1().Secrets("runtime-prod").List(t.Context(), meta.ListOptions{})
	if err != nil || len(secrets.Items) != 2 {
		t.Fatal("unknown process death removed inputs")
	}
}

func TestKubernetesRestartAndReuseFence(t *testing.T) {
	m, root, client := kubeManager(t)
	if err := os.WriteFile(filepath.Join(root, "runtime.auth"), []byte("HUB_RUNTIME_AUTH=synthetic-runtime\nPRIVATE_PROVIDER=provider-canary\nHUB_TOOLHUB_CONTROL=control-canary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	k := m.backend.(*kubernetesBackend)
	secret, err := client.CoreV1().Secrets("runtime-prod").Get(t.Context(), k.secretName(first), meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range secret.Data {
		if string(value) == "provider-canary" || string(value) == "control-canary" {
			t.Fatal("control/provider credential projected into runtime")
		}
	}
	lock := m.lockFor(runtimeKey(binding(root)))
	lock.Lock()
	changed, err := m.restartKubernetesLocked(t.Context(), first)
	lock.Unlock()
	if err != nil || !changed {
		t.Fatalf("restart: %v", err)
	}
	second, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if second.RuntimeID != first.RuntimeID || second.Generation == first.Generation || second.ComputeID == first.ComputeID {
		t.Fatal("restart lost logical identity or compute fencing")
	}
	pod, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), second.Container, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = "foreign-replacement"
	if err := client.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, "runtime-prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("hot reuse ignored immutable Pod UID")
	}
}

func TestKubernetesTerminationEvidence(t *testing.T) {
	base := &core.Pod{Spec: core.PodSpec{NodeName: "node", RestartPolicy: core.RestartPolicyNever, Containers: []core.Container{{Name: "runtime"}}, InitContainers: []core.Container{{Name: "prepare"}}}, Status: core.PodStatus{Phase: core.PodFailed, InitContainerStatuses: []core.ContainerStatus{{Name: "prepare", State: core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 1, FinishedAt: meta.Now(), Reason: "Error"}}}}, ContainerStatuses: []core.ContainerStatus{{Name: "runtime", State: core.ContainerState{Waiting: &core.ContainerStateWaiting{Reason: "PodInitializing"}}}}}}
	for _, scenario := range []struct {
		name    string
		change  func(*core.Pod)
		stopped bool
	}{
		{"failed init before runtime", func(*core.Pod) {}, true},
		{"node loss", func(p *core.Pod) { p.Status.Reason = "NodeLost" }, false},
		{"unknown init", func(p *core.Pod) {
			p.Status.InitContainerStatuses[0].State.Terminated.Reason = "ContainerStatusUnknown"
		}, false},
		{"runtime already started", func(p *core.Pod) { p.Status.ContainerStatuses[0].ContainerID = "containerd://started" }, false},
		{"restarting init", func(p *core.Pod) { p.Spec.RestartPolicy = core.RestartPolicyAlways }, false},
		{"unscheduled", func(p *core.Pod) { p.Spec.NodeName = "" }, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			p := base.DeepCopy()
			scenario.change(p)
			if podTerminationConfirmed(p) != scenario.stopped {
				t.Fatal("incorrect process-death evidence")
			}
		})
	}
}

func kubeCreates(client *fake.Clientset, resource string) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "create" && action.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestKubernetesLifecycleRetainsIdentityAndPVC(t *testing.T) {
	m, root, client := kubeManager(t)
	first, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), first.Container, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "sandbox-test" || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC {
		t.Fatal("runtime class or isolation contract missing")
	}
	claimName := m.backend.(*kubernetesBackend).claimName(first)
	claim, err := client.CoreV1().PersistentVolumeClaims("runtime-prod").Get(t.Context(), claimName, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, variable := range pod.Spec.Containers[0].Env {
		field := variable.ValueFrom != nil && variable.ValueFrom.FieldRef != nil && (variable.Name == "HUB_KUBERNETES_POD_NAME" || variable.Name == "HUB_KUBERNETES_POD_NAMESPACE")
		if variable.Value != "" || variable.ValueFrom == nil || variable.ValueFrom.SecretKeyRef == nil && !field {
			t.Fatal("runtime environment escaped Secret projection")
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil {
			t.Fatal("runtime has a hostPath")
		}
	}
	client.PrependReactor("delete", "pods", func(action ktest.Action) (bool, runtime.Object, error) {
		options := action.(ktest.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID == "" {
			t.Error("Pod deletion is not fenced by its immutable UID")
		}
		return false, nil, nil
	})
	if err := m.ReleaseBinding(binding(root)); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(t.Context(), m.cfg.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), first.Container, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("idle Pod survived reap")
	}
	if secrets, err := client.CoreV1().Secrets("runtime-prod").List(t.Context(), meta.ListOptions{}); err != nil || len(secrets.Items) != 0 {
		t.Fatal("generation credentials survived reap")
	}
	retained, err := client.CoreV1().PersistentVolumeClaims("runtime-prod").Get(t.Context(), claimName, meta.GetOptions{})
	if err != nil || retained.UID != claim.UID {
		t.Fatal("reap replaced or deleted space PVC")
	}
	second, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	if second.RuntimeID != first.RuntimeID || second.Generation == first.Generation || second.ComputeID == first.ComputeID || kubeCreates(client, "persistentvolumeclaims") != 1 {
		t.Fatal("respawn lost identity or replaced persistent data")
	}
	if !strings.HasPrefix(second.Address, "http://10.244.") {
		t.Fatal("runtime still uses host-published Docker port")
	}
}

func TestKubernetesForeignPodCannotReceiveCredentials(t *testing.T) {
	m, root, client := kubeManager(t)
	name := containerName(runtimeKey(binding(root)))
	foreign := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: name, UID: "foreign", Annotations: map[string]string{kubeOwner: "another-owner"}}}
	if _, err := client.CoreV1().Pods("runtime-prod").Create(t.Context(), foreign, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	if _, err := m.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("adopted foreign Pod")
	}
	for _, resource := range []string{"secrets", "persistentvolumeclaims", "networkpolicies"} {
		if kubeCreates(client, resource) != 0 {
			t.Fatalf("foreign Pod caused %s mutation", resource)
		}
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("deleted a foreign object")
		}
	}
}

func TestKubernetesConcurrentEnsureAndAdministrativeHold(t *testing.T) {
	m, root, client := kubeManager(t)
	m.cfg.AdminAuth = "synthetic-admin"
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if _, err := m.Ensure(t.Context(), binding(root)); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if kubeCreates(client, "pods") != 2 {
		t.Fatal("parallel wake created multiple runtime Pods")
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{"secret", http.StatusForbidden}, {"synthetic-admin", http.StatusOK}} {
		request := httptest.NewRequest(http.MethodPost, "/v1/admin/holds", strings.NewReader(`{"principal_id":"alice","context_id":"alice","reason":"manual"}`))
		request.Header.Set("Authorization", "Bearer "+tc.token)
		response := httptest.NewRecorder()
		m.Handler().ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Fatalf("hold status=%d want=%d", response.Code, tc.want)
		}
	}
	if _, err := m.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("held scope spawned")
	}
	if kubeCreates(client, "pods") != 2 {
		t.Fatal("hold caused respawn")
	}
	claims, err := client.CoreV1().PersistentVolumeClaims("runtime-prod").List(t.Context(), meta.ListOptions{})
	if err != nil || len(claims.Items) != 1 {
		t.Fatal("hold removed persistent data")
	}
}

func TestKubernetesUIDRaceAndPodConditions(t *testing.T) {
	m, root, client := kubeManager(t)
	rt, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), rt.Container, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.Phase = core.PodPending
	pod.Status.Message = "private-provider-message"
	pod.Status.Conditions = []core.PodCondition{{Type: core.PodScheduled, Status: core.ConditionFalse, Reason: "private-reason", Message: "private-condition-message"}}
	if _, err := client.CoreV1().Pods("runtime-prod").UpdateStatus(t.Context(), pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(t.Context(), m.cfg.Now())
	view := m.StatusView()
	if len(view.Runtimes) != 1 || view.Runtimes[0].Phase != PhasePending {
		t.Fatal("pending Pod phase was not mapped")
	}
	var scheduled bool
	for _, condition := range view.Runtimes[0].Conditions {
		if condition.Name == "pod_scheduled" {
			scheduled = true
			if condition.OK {
				t.Fatal("unscheduled Pod marked ready")
			}
		}
	}
	if !scheduled {
		t.Fatal("Pod conditions absent")
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-") {
		t.Fatal("raw Pod diagnostics crossed status boundary")
	}
	client.PrependReactor("delete", "pods", func(action ktest.Action) (bool, runtime.Object, error) {
		pod.UID = "replacement"
		if err := client.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), pod, "runtime-prod"); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, rt.Container, fmt.Errorf("UID changed"))
	})
	if err := m.removeOwnedRuntime(t.Context(), rt); err == nil {
		t.Fatal("UID race accepted as successful removal")
	}
	remaining, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), rt.Container, meta.GetOptions{})
	if err != nil || remaining.UID != "replacement" {
		t.Fatal("replacement Pod deleted")
	}
}

func TestKubernetesRelayLossDegradesReadyRuntime(t *testing.T) {
	m, root, client := kubeManager(t)
	rt, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	k := m.backend.(*kubernetesBackend)
	relay, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), k.relayRuntime(rt).Container, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	relay.Status.Conditions = []core.PodCondition{{Type: core.PodReady, Status: core.ConditionFalse}}
	if _, err := client.CoreV1().Pods("runtime-prod").UpdateStatus(t.Context(), relay, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	m.ReconcileHealth(t.Context(), m.cfg.Now())
	view := m.StatusView()
	if len(view.Runtimes) != 1 || view.Runtimes[0].Phase != PhaseDegraded {
		t.Fatal("healthy Hermes concealed failed private relay")
	}
	found := false
	for _, condition := range view.Runtimes[0].Conditions {
		if condition.Name == "relays_ready" {
			found = true
			if condition.OK {
				t.Fatal("failed relay reported ready")
			}
		}
	}
	if !found {
		t.Fatal("relay condition missing")
	}
}

func TestKubernetesBackendSwitchRequiresFencedCompute(t *testing.T) {
	m, root, _ := kubeManager(t)
	if _, err := m.Ensure(t.Context(), binding(root)); err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.ExecutionBackend = "docker"
	if _, err := New(cfg); err == nil {
		t.Fatal("live Kubernetes compute was adopted by Docker")
	}
	if err := m.ReleaseBinding(binding(root)); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(t.Context(), m.cfg.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg); err != nil {
		t.Fatalf("stopped backend switch: %v", err)
	}
}

func TestKubernetesInputBoundsAndSymlinkIsolation(t *testing.T) {
	m, root, client := kubeManager(t)
	if err := os.WriteFile(filepath.Join(root, "managed-runtime.prod.env"), []byte("OVERSIZE="+strings.Repeat("x", kubeInputBudget+1)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(t.Context(), binding(root)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if kubeCreates(client, "secrets") != 0 || kubeCreates(client, "pods") != 0 {
		t.Fatal("oversized input mutated runtime resources")
	}
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte("outside-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "escape")); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	anchor, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	inputs := kubeInputs{root: anchor, data: map[string][]byte{}}
	if err := inputs.snapshot("escape", "projection"); err == nil || len(inputs.data) != 0 {
		t.Fatal("snapshot followed an escaping symlink")
	}
}

func TestKubernetesSpacesNeverShareClaimsOrNetworkSelectors(t *testing.T) {
	m, root, client := kubeManager(t)
	alice, err := m.Ensure(t.Context(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	bobRoot := filepath.Join(m.cfg.SpacesRoot, "bob")
	if err := os.MkdirAll(bobRoot, 0700); err != nil {
		t.Fatal(err)
	}
	publishSpaceRevision(t, bobRoot, "bob", nil)
	writeHermesSources(t, bobRoot)
	for name, content := range map[string]string{"runtime.auth": "HUB_RUNTIME_AUTH=synthetic-bob\n", "managed-runtime.prod.env": "HUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"} {
		if err := os.WriteFile(filepath.Join(bobRoot, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bobBinding := Binding{PrincipalID: "bob", ContextID: "bob", RuntimeID: "bob", RuntimeMode: "gateway", UserID: "bob", OrganizationID: "personal", ContextRoot: bobRoot, PolicyVersion: "policy-bob"}
	bob, err := m.Ensure(t.Context(), bobBinding)
	if err != nil {
		t.Fatal(err)
	}
	k := m.backend.(*kubernetesBackend)
	if k.claimName(alice) == k.claimName(bob) || k.labels(alice)[kubeScope] == k.labels(bob)[kubeScope] {
		t.Fatal("different spaces share storage or network selector")
	}
	for _, rt := range []Runtime{alice, bob} {
		pod, err := client.CoreV1().Pods("runtime-prod").Get(t.Context(), rt.Container, meta.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName != k.claimName(rt) {
				t.Fatal("Pod mounts another space's claim")
			}
		}
		policy, err := client.NetworkingV1().NetworkPolicies("runtime-prod").Get(t.Context(), rt.Container, meta.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if policy.Spec.PodSelector.MatchLabels[kubeScope] != k.labels(rt)[kubeScope] {
			t.Fatal("policy selects another space")
		}
		for _, rule := range policy.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock != nil || peer.PodSelector == nil || peer.NamespaceSelector == nil {
					t.Fatal("runtime policy permits unrestricted egress")
				}
			}
		}
	}
}
