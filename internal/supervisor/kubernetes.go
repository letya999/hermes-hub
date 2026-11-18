package supervisor

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type KubernetesConfig struct {
	RelayImage       string
	ToolHubURL       string
	CliproxyURL      string
	Namespace        string
	Kubeconfig       string
	RuntimeClassName string
	StorageClass     string
	StorageSize      string
	ControlNamespace string
	ControlRelease   string
	ImagePullSecrets []string
	Client           kubernetes.Interface
}

type kubernetesBackend struct {
	m      *Manager
	cfg    KubernetesConfig
	client kubernetes.Interface
}

const kubeOwner = "hermes-hub.io/owner"
const kubeScope = "hermes-hub.io/scope"
const kubeContext = "hermes-hub.io/context"
const kubeGeneration = "hermes-hub.io/generation"
const kubeRole = "hermes-hub.io/role"
const kubeComputeFence = "hermes-hub.io/compute-fence"

func newKubernetesBackend(m *Manager) (*kubernetesBackend, error) {
	cfg := m.cfg.Kubernetes
	if cfg.Namespace == "" {
		cfg.Namespace = envOr("HUB_KUBERNETES_NAMESPACE", "hermes-hub-"+m.environment())
	}
	if cfg.RuntimeClassName == "" {
		cfg.RuntimeClassName = os.Getenv("HUB_RUNTIME_CLASS_NAME")
	}
	if cfg.RuntimeClassName == "" && m.cfg.ContainerRuntime != "runc" {
		cfg.RuntimeClassName = m.cfg.ContainerRuntime
	}
	if m.cfg.ContainerRuntime != "runc" && (cfg.RuntimeClassName == "runc" || cfg.RuntimeClassName == "runsc" || cfg.RuntimeClassName == "kata") && cfg.RuntimeClassName != m.cfg.ContainerRuntime {
		return nil, errors.New("selected RuntimeClass conflicts with container runtime")
	}
	if cfg.StorageClass == "" {
		cfg.StorageClass = os.Getenv("HUB_KUBERNETES_STORAGE_CLASS")
	}
	if cfg.StorageSize == "" {
		cfg.StorageSize = envOr("HUB_KUBERNETES_STORAGE_SIZE", "10Gi")
	}
	if cfg.ControlNamespace == "" {
		cfg.ControlNamespace = envOr("HUB_KUBERNETES_CONTROL_NAMESPACE", cfg.Namespace)
	}
	if cfg.ControlRelease == "" {
		cfg.ControlRelease = envOr("HUB_KUBERNETES_CONTROL_RELEASE", "hermes-hub")
	}
	if cfg.RelayImage == "" {
		cfg.RelayImage = envOr("HUB_KUBERNETES_RELAY_IMAGE", m.cfg.ScriptImage)
	}
	if cfg.RelayImage == "" {
		cfg.RelayImage = m.cfg.Image
	}
	if cfg.ToolHubURL == "" {
		cfg.ToolHubURL = envOr("HUB_KUBERNETES_TOOLHUB_URL", "http://"+cfg.ControlRelease+"-hermes-hub-toolhub."+cfg.ControlNamespace+".svc:8090")
	}
	if cfg.CliproxyURL == "" {
		cfg.CliproxyURL = envOr("HUB_KUBERNETES_CLIPROXY_URL", "http://"+cfg.ControlRelease+"-hermes-hub-cliproxy."+cfg.ControlNamespace+".svc:8317")
	}
	for _, origin := range []string{cfg.ToolHubURL, cfg.CliproxyURL} {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
			return nil, errors.New("invalid Kubernetes relay upstream origin")
		}
	}
	if cfg.Kubeconfig == "" {
		cfg.Kubeconfig = os.Getenv("HUB_KUBECONFIG")
	}
	if len(cfg.ImagePullSecrets) == 0 && os.Getenv("HUB_KUBERNETES_PULL_SECRETS") != "" {
		cfg.ImagePullSecrets = strings.Split(os.Getenv("HUB_KUBERNETES_PULL_SECRETS"), ",")
	}
	if len(validation.IsDNS1123Label(cfg.Namespace)) != 0 || len(validation.IsDNS1123Label(cfg.ControlNamespace)) != 0 || len(validation.IsValidLabelValue(cfg.ControlRelease)) != 0 || cfg.RuntimeClassName != "" && len(validation.IsDNS1123Subdomain(cfg.RuntimeClassName)) != 0 {
		return nil, errors.New("invalid Kubernetes namespace or runtime class")
	}
	if size, err := resource.ParseQuantity(cfg.StorageSize); err != nil || size.Sign() <= 0 {
		return nil, errors.New("invalid Kubernetes storage size")
	}
	for _, name := range cfg.ImagePullSecrets {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return nil, errors.New("invalid Kubernetes image pull Secret reference")
		}
	}
	client := cfg.Client
	if client == nil {
		var connection *rest.Config
		var err error
		if cfg.Kubeconfig != "" {
			connection, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
		} else {
			connection, err = rest.InClusterConfig()
		}
		if err != nil {
			return nil, errors.New("kubernetes client configuration unavailable")
		}
		if connection.Insecure || !strings.HasPrefix(connection.Host, "https://") {
			return nil, errors.New("kubernetes API requires verified TLS")
		}
		connection.Timeout, connection.QPS, connection.Burst = 15*time.Second, 10, 20
		client, err = kubernetes.NewForConfig(connection)
		if err != nil {
			return nil, errors.New("kubernetes client unavailable")
		}
	}
	m.cfg.Kubernetes = cfg
	return &kubernetesBackend{m: m, cfg: cfg, client: client}, nil
}

func (k *kubernetesBackend) scope(rt Runtime) string {
	return hex.EncodeToString(hashBytes(rt.PrincipalID + "\x00" + rt.ContextID))
}
func (k *kubernetesBackend) annotations(rt Runtime, generation bool) map[string]string {
	result := map[string]string{kubeOwner: k.m.ownerID(), kubeScope: k.scope(rt)}
	if generation {
		result[kubeGeneration] = rt.Generation
		result[kubeContext] = hex.EncodeToString(hashBytes(rt.PrincipalID + "\x00" + rt.ContextID + "\x00" + rt.RuntimeMode))
	}
	return result
}
func (k *kubernetesBackend) labels(rt Runtime) map[string]string {
	return map[string]string{kubeOwner: k.m.ownerID()[:32], kubeScope: k.scope(rt)[:32], kubeContext: k.annotations(rt, true)[kubeContext][:32], "app.kubernetes.io/component": "runtime", "app.kubernetes.io/name": "hermes-hub", "app.kubernetes.io/instance": k.cfg.ControlRelease, kubeGeneration: rt.Generation, "hermes-hub.io/environment": k.m.environment()}
}
func (k *kubernetesBackend) claimName(rt Runtime) string { return "space-" + k.scope(rt)[:32] }
func (k *kubernetesBackend) secretName(rt Runtime) string {
	return rt.Container + "-" + hex.EncodeToString(hashBytes(rt.Generation))[:12]
}

func (k *kubernetesBackend) owned(object meta.Object, rt Runtime, generation bool) bool {
	for key, value := range k.annotations(rt, generation) {
		if object.GetAnnotations()[key] != value {
			return false
		}
	}
	return rt.Generation != "" && (!generation || rt.ComputeID == "" || string(object.GetUID()) == rt.ComputeID)
}

func (k *kubernetesBackend) pod(ctx context.Context, rt Runtime) (*core.Pod, error) {
	pod, err := k.client.CoreV1().Pods(k.cfg.Namespace).Get(ctx, rt.Container, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrRuntimeMissing
	}
	if err != nil {
		return nil, errors.New("runtime Pod observation unavailable")
	}
	return pod, nil
}

func (k *kubernetesBackend) Verify(ctx context.Context, rt Runtime) error {
	pod, err := k.pod(ctx, rt)
	if err != nil {
		return err
	}
	if !k.owned(pod, rt, true) {
		return errors.New("runtime Pod ownership mismatch")
	}
	if !k.podIsolationMatches(pod) {
		return errors.New("runtime Pod isolation mismatch")
	}
	return nil
}

func (k *kubernetesBackend) podIsolationMatches(pod *core.Pod) bool {
	class := ""
	if pod.Spec.RuntimeClassName != nil {
		class = *pod.Spec.RuntimeClassName
	}
	return class == k.cfg.RuntimeClassName
}

func podObservation(pod *core.Pod, port int) ComputeObservation {
	result := ComputeObservation{ID: string(pod.UID)}
	if pod.Status.PodIP != "" {
		result.Address = "http://" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(port))
	}
	switch pod.Status.Phase {
	case core.PodPending:
		result.State = "created"
	case core.PodRunning:
		result.State = "running"
	case core.PodFailed, core.PodSucceeded:
		result.State = "exited"
	default:
		result.State = "unknown"
	}
	if pod.DeletionTimestamp != nil {
		result.State = "stopping"
	}
	for _, expected := range []struct {
		kind core.PodConditionType
		name string
	}{{core.PodScheduled, "pod_scheduled"}, {core.PodInitialized, "pod_initialized"}, {core.ContainersReady, "pod_containers_ready"}, {core.PodReady, "pod_ready"}} {
		condition := Condition{Name: expected.name, Reason: ReasonNotChecked}
		for _, observed := range pod.Status.Conditions {
			if observed.Type == expected.kind {
				condition.OK = observed.Status == core.ConditionTrue
				condition.Since = observed.LastTransitionTime.Time
				condition.Reason = ReasonReadinessUnavailable
				if condition.OK {
					condition.Reason = ReasonReady
				}
			}
		}
		result.Conditions = append(result.Conditions, condition)
	}
	return result
}

func (k *kubernetesBackend) Observe(ctx context.Context, rt Runtime) (ComputeObservation, error) {
	pod, err := k.pod(ctx, rt)
	if err != nil {
		return ComputeObservation{}, err
	}
	if !k.owned(pod, rt, true) {
		return ComputeObservation{}, errors.New("runtime Pod ownership mismatch")
	}
	if !k.podIsolationMatches(pod) {
		return ComputeObservation{}, errors.New("runtime Pod isolation mismatch")
	}
	result := podObservation(pod, k.m.cfg.RuntimePort)
	relay, relayErr := k.pod(ctx, k.relayRuntime(rt))
	condition := Condition{Name: "relays_ready", Reason: ReasonReadinessUnavailable}
	if relayErr == nil && k.owned(relay, k.relayRuntime(rt), true) && relay.Annotations[kubeRole] == "relay" {
		for _, observed := range podObservation(relay, k.m.cfg.RuntimePort).Conditions {
			if observed.Name == "pod_ready" {
				condition.OK, condition.Since = observed.OK && relay.DeletionTimestamp == nil, observed.Since
			}
		}
	}
	if condition.OK {
		condition.Reason = ReasonReady
	}
	result.Conditions = append(result.Conditions, condition)
	return result, nil
}

func (m *Manager) prepareKubernetesReplacement(ctx context.Context, key, name string) error {
	m.mu.Lock()
	previous := m.items[key]
	var rt Runtime
	if previous != nil {
		rt = previous.Runtime
	}
	m.mu.Unlock()
	if previous != nil {
		return m.backend.Remove(ctx, rt)
	}
	k := m.backend.(*kubernetesBackend)
	for _, candidate := range []string{name, name + "-relay"} {
		if _, err := k.pod(ctx, Runtime{Container: candidate}); !errors.Is(err, ErrRuntimeMissing) {
			return errors.New("untracked runtime Pod blocks allocation")
		}
	}
	return nil
}

func (k *kubernetesBackend) Spawn(ctx context.Context, request SpawnRequest) (ComputeObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	for _, rt := range []Runtime{request.Runtime, k.relayRuntime(request.Runtime)} {
		if existing, err := k.pod(ctx, rt); err == nil {
			expectedRole := ""
			if rt.Container != request.Runtime.Container {
				expectedRole = "relay"
			}
			if !k.owned(existing, rt, true) || existing.Annotations[kubeRole] != expectedRole {
				return ComputeObservation{}, errors.New("runtime Pod ownership mismatch")
			}
		} else if !errors.Is(err, ErrRuntimeMissing) {
			return ComputeObservation{}, err
		}
	}
	main, secret, err := k.plan(request)
	if err != nil {
		return ComputeObservation{}, err
	}
	relay, relaySecret, err := k.relayPlan(request, main, secret)
	if err != nil {
		return ComputeObservation{}, err
	}
	if err = k.ensureStorage(ctx, request.Runtime); err != nil {
		return ComputeObservation{}, err
	}
	if err = k.ensurePolicy(ctx, request.Runtime); err != nil {
		return ComputeObservation{}, err
	}
	if err = k.createSecret(ctx, relaySecret, request.Runtime); err != nil {
		return ComputeObservation{}, err
	}
	readyRelay, err := k.startRelay(ctx, relay, request.Runtime)
	if err != nil {
		return ComputeObservation{}, err
	}
	if err = k.bindPolicyReceipt(ctx, main, secret, request.Runtime, readyRelay.Status.PodIP); err != nil {
		return ComputeObservation{}, err
	}
	if err = k.createSecret(ctx, secret, request.Runtime); err != nil {
		return ComputeObservation{}, err
	}
	if _, err = k.client.CoreV1().Pods(k.cfg.Namespace).Create(ctx, main, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		if k.cfg.RuntimeClassName != "" {
			return ComputeObservation{}, errors.New("runtime Pod admission failed; verify selected RuntimeClass and namespace policy")
		}
		return ComputeObservation{}, errors.New("runtime Pod creation failed")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		observation, err := k.Observe(ctx, request.Runtime)
		if err != nil {
			return ComputeObservation{}, err
		}
		if observation.State == "exited" {
			return ComputeObservation{}, errors.New("runtime Pod exited during spawn")
		}
		if observation.Address != "" && observation.State == "running" {
			return observation, nil
		}
		select {
		case <-ctx.Done():
			return ComputeObservation{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *kubernetesBackend) ensureStorage(ctx context.Context, rt Runtime) error {
	client := k.client.CoreV1().PersistentVolumeClaims(k.cfg.Namespace)
	claim := &core.PersistentVolumeClaim{ObjectMeta: meta.ObjectMeta{Name: k.claimName(rt), Annotations: k.annotations(rt, false), Labels: k.labels(rt)}, Spec: core.PersistentVolumeClaimSpec{AccessModes: []core.PersistentVolumeAccessMode{core.ReadWriteOnce}, Resources: core.VolumeResourceRequirements{Requests: core.ResourceList{core.ResourceStorage: resource.MustParse(k.cfg.StorageSize)}}}}
	if k.cfg.StorageClass != "" {
		claim.Spec.StorageClassName = &k.cfg.StorageClass
	}
	existing, err := client.Get(ctx, claim.Name, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		existing, err = client.Create(ctx, claim, meta.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			existing, err = client.Get(ctx, claim.Name, meta.GetOptions{})
		}
	}
	if err != nil {
		return errors.New("space PVC unavailable")
	}
	if !k.owned(existing, rt, false) || existing.Spec.Resources.Requests.Storage().Cmp(resource.MustParse(k.cfg.StorageSize)) < 0 {
		return errors.New("space PVC ownership or capacity mismatch")
	}
	if k.cfg.StorageClass != "" && (existing.Spec.StorageClassName == nil || *existing.Spec.StorageClassName != k.cfg.StorageClass) {
		return errors.New("space PVC storage class mismatch")
	}
	return nil
}

func (k *kubernetesBackend) peer(component string) networking.NetworkPolicyPeer {
	selector := map[string]string{"app.kubernetes.io/name": "hermes-hub", "app.kubernetes.io/component": component}
	if k.cfg.ControlRelease != "" {
		selector["app.kubernetes.io/instance"] = k.cfg.ControlRelease
	}
	return networking.NetworkPolicyPeer{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": k.cfg.ControlNamespace}}, PodSelector: &meta.LabelSelector{MatchLabels: selector}}
}

func kubePort(port int32, protocol core.Protocol) networking.NetworkPolicyPort {
	value := intstr.FromInt32(port)
	return networking.NetworkPolicyPort{Protocol: &protocol, Port: &value}
}

func (k *kubernetesBackend) policy(rt Runtime) *networking.NetworkPolicy {
	selector := k.labels(rt)
	annotations := k.annotations(rt, false)
	annotations[kubeContext] = k.annotations(rt, true)[kubeContext]
	policy := &networking.NetworkPolicy{ObjectMeta: meta.ObjectMeta{Name: rt.Container, Annotations: annotations, Labels: k.labels(rt)}, Spec: networking.NetworkPolicySpec{PodSelector: meta.LabelSelector{MatchLabels: selector}, PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}, Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{k.peer("supervisor")}, Ports: []networking.NetworkPolicyPort{kubePort(int32(k.m.cfg.RuntimePort), core.ProtocolTCP)}}}}} // #nosec G115 -- runtime port is normalized to 1-65535 in supervisor config
	policy.Spec.Egress = []networking.NetworkPolicyEgressRule{{To: []networking.NetworkPolicyPeer{{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &meta.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networking.NetworkPolicyPort{kubePort(53, core.ProtocolUDP), kubePort(53, core.ProtocolTCP)}}}
	relay := k.labels(rt)
	relay["app.kubernetes.io/component"] = "runtime-relay"
	policy.Spec.Egress = append(policy.Spec.Egress, networking.NetworkPolicyEgressRule{To: []networking.NetworkPolicyPeer{{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": k.cfg.Namespace}}, PodSelector: &meta.LabelSelector{MatchLabels: relay}}}, Ports: []networking.NetworkPolicyPort{kubePort(8090, core.ProtocolTCP), kubePort(8318, core.ProtocolTCP), kubePort(8319, core.ProtocolTCP)}})
	return policy
}

func (k *kubernetesBackend) ensurePolicy(ctx context.Context, rt Runtime) error {
	guard := &networking.NetworkPolicy{ObjectMeta: meta.ObjectMeta{Name: k.claimName(rt) + "-guard", Annotations: k.annotations(rt, false)}, Spec: networking.NetworkPolicySpec{PodSelector: meta.LabelSelector{MatchLabels: map[string]string{kubeOwner: k.m.ownerID()[:32], kubeScope: k.scope(rt)[:32]}}, PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}}}
	for _, wanted := range []*networking.NetworkPolicy{guard, k.policy(rt), k.relayPolicy(rt)} {
		if err := k.ensurePolicyObject(ctx, rt, wanted); err != nil {
			return err
		}
	}
	return nil
}
func (k *kubernetesBackend) ensurePolicyObject(ctx context.Context, rt Runtime, wanted *networking.NetworkPolicy) error {
	client := k.client.NetworkingV1().NetworkPolicies(k.cfg.Namespace)
	existing, err := client.Get(ctx, wanted.Name, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.Create(ctx, wanted, meta.CreateOptions{})
		if err == nil {
			return nil
		}
		if apierrors.IsAlreadyExists(err) {
			existing, err = client.Get(ctx, wanted.Name, meta.GetOptions{})
		}
	}
	if err != nil {
		return errors.New("space NetworkPolicy unavailable")
	}
	if !k.owned(existing, rt, false) || existing.Annotations[kubeContext] != wanted.Annotations[kubeContext] {
		return errors.New("space NetworkPolicy ownership mismatch")
	}
	existing.Spec, existing.Labels, existing.Annotations = wanted.Spec, wanted.Labels, wanted.Annotations
	if _, err = client.Update(ctx, existing, meta.UpdateOptions{}); err != nil {
		return errors.New("space NetworkPolicy update failed")
	}
	return nil
}

func deleteUID(uid types.UID) meta.DeleteOptions {
	return meta.DeleteOptions{Preconditions: &meta.Preconditions{UID: &uid}}
}

func (k *kubernetesBackend) Remove(ctx context.Context, rt Runtime) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, owned := range []Runtime{rt, k.relayRuntime(rt)} {
		if err := k.removePod(ctx, owned); err != nil {
			return err
		}
	}
	for _, name := range []string{k.secretName(rt), k.relaySecretName(rt)} {
		secret, err := k.client.CoreV1().Secrets(k.cfg.Namespace).Get(ctx, name, meta.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return errors.New("runtime Secret cleanup unavailable")
		}
		withoutID := rt
		withoutID.ComputeID = ""
		if !k.owned(secret, withoutID, true) {
			return errors.New("runtime Secret ownership mismatch")
		}
		if err = k.client.CoreV1().Secrets(k.cfg.Namespace).Delete(ctx, secret.Name, deleteUID(secret.UID)); err != nil && !apierrors.IsNotFound(err) {
			return errors.New("runtime Secret deletion failed")
		}
	}
	return nil
}

func podTerminationConfirmed(pod *core.Pod) bool {
	if pod.Spec.NodeName == "" {
		return true // Unscheduled Pods cannot have started a process.
	}
	if pod.Status.Reason == "NodeLost" || pod.Status.Reason == "Shutdown" {
		return false
	}
	// With Never restart, a terminal failed init cannot have started the runtime.
	if pod.Spec.RestartPolicy == core.RestartPolicyNever && pod.Status.Phase == core.PodFailed && len(pod.Status.InitContainerStatuses) != 0 {
		last := pod.Status.InitContainerStatuses[len(pod.Status.InitContainerStatuses)-1].State.Terminated
		if last != nil && last.ExitCode != 0 && !last.FinishedAt.IsZero() && last.Reason != "ContainerStatusUnknown" && last.Reason != "NodeLost" {
			neverStarted := true
			for _, status := range pod.Status.ContainerStatuses {
				neverStarted = neverStarted && status.ContainerID == "" && status.RestartCount == 0 && status.State.Running == nil && status.State.Terminated == nil
			}
			if neverStarted {
				return true
			}
		}
	}
	if len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) {
		return false
	}
	for _, status := range append(slices.Clone(pod.Status.ContainerStatuses), pod.Status.InitContainerStatuses...) {
		ended := status.State.Terminated
		if ended == nil || ended.FinishedAt.IsZero() || ended.Reason == "ContainerStatusUnknown" || ended.Reason == "NodeLost" {
			return false
		}
	}
	return true
}

func (k *kubernetesBackend) removePod(ctx context.Context, rt Runtime) error {
	pod, err := k.pod(ctx, rt)
	if errors.Is(err, ErrRuntimeMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if !k.owned(pod, rt, true) {
		return errors.New("runtime Pod ownership mismatch")
	}
	if err = k.client.CoreV1().Pods(k.cfg.Namespace).Delete(ctx, pod.Name, deleteUID(pod.UID)); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("runtime Pod deletion failed")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := k.pod(ctx, rt)
		if errors.Is(err, ErrRuntimeMissing) {
			return nil
		}
		if err != nil {
			return err
		}
		if current.UID != pod.UID {
			return errors.New("runtime Pod replaced during deletion")
		}
		if current.DeletionTimestamp != nil && slices.Contains(current.Finalizers, kubeComputeFence) && podTerminationConfirmed(current) {
			current.Finalizers = slices.DeleteFunc(current.Finalizers, func(value string) bool { return value == kubeComputeFence })
			if _, err = k.client.CoreV1().Pods(k.cfg.Namespace).Update(ctx, current, meta.UpdateOptions{}); err != nil {
				return errors.New("runtime termination fence release failed")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *kubernetesBackend) inventory(ctx context.Context) ([]OrphanRuntime, error) {
	pods, err := k.client.CoreV1().Pods(k.cfg.Namespace).List(ctx, meta.ListOptions{LabelSelector: kubeOwner + "=" + k.m.ownerID()[:32], Limit: 257})
	if err != nil || len(pods.Items) > 256 || pods.Continue != "" {
		return nil, errors.New("runtime Pod inventory unavailable or over limit")
	}
	var result []OrphanRuntime
	for _, pod := range pods.Items {
		if pod.Annotations[kubeRole] == "relay" {
			continue
		}
		if pod.Annotations[kubeOwner] != k.m.ownerID() {
			continue
		}
		if len(pod.Annotations[kubeContext]) != 64 || pod.Annotations[kubeGeneration] == "" {
			return nil, errors.New("invalid runtime Pod inventory ownership")
		}
		result = append(result, OrphanRuntime{Container: string(pod.UID), Name: pod.Name, ContextKey: pod.Annotations[kubeContext], Generation: pod.Annotations[kubeGeneration]})
	}
	return result, nil
}

func (k *kubernetesBackend) runtimeLabels(ctx context.Context, name string) (map[string]string, error) {
	pod, err := k.pod(ctx, Runtime{Container: name})
	if err != nil {
		return nil, err
	}
	return map[string]string{"hermes-hub.owner": pod.Annotations[kubeOwner], "hermes-hub.context": pod.Annotations[kubeContext], "hermes-hub.generation": pod.Annotations[kubeGeneration]}, nil
}
