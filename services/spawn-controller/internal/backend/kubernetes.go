// Kubernetes launch backend, moved from v0 internal/supervisor/
// kubernetes.go + kubernetes_restart.go (CHG-0336 S8). Compile-only: no
// env var or flag constructs it until M40.15 selects it behind
// backend.Backend. The pod contract is the v0 one verbatim — UID-fenced
// ownership, retained space PVC, NetworkPolicy admission — consuming the
// resolved backend.Spec where the v0 request carried binding+settings.
package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

// KubernetesConfig wires the pod backend: the v0 KubernetesConfig fields
// plus the values the supervisor supplied from its own config
// (Environment, OwnerID, SpacesRoot, RuntimePort, image fallbacks, OCI
// runtime class source, resource limits, control-plane credential).
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
	// Supervisor-derived wiring (v0 m.cfg / m.environment() / m.ownerID()).
	Environment      string
	OwnerID          string
	SpacesRoot       string
	RuntimePort      int
	Image            string
	ScriptImage      string
	ContainerRuntime string
	CPULimit         string
	MemoryLimit      string
	RuntimeAuth      string
}

// Kubernetes is the compile-only pod backend (M40.15).
type Kubernetes struct {
	cfg    KubernetesConfig
	client kubernetes.Interface
}

const kubeOwner = "hermes-hub.io/owner"
const kubeScope = "hermes-hub.io/scope"
const kubeContext = "hermes-hub.io/context"
const kubeGeneration = "hermes-hub.io/generation"
const kubeRole = "hermes-hub.io/role"
const kubeComputeFence = "hermes-hub.io/compute-fence"

// kubeWorkload is the managed-runtime identity the moved pod code fences
// on (v0 supervisor Runtime): scope annotations bind owner+space; the
// generation and pod UID carry the compute fence. Start builds it from
// the Spec; Inspect/Stop re-derive it from the pod's own annotations —
// the v0 principal/context scope check needs the claim row's tenant,
// which only M40.15's selection path supplies.
type kubeWorkload struct {
	name       string // pod name (v0 rt.Container, Spec.Key in v1)
	generation string // v0 rt.Generation, Spec generation label in v1
	uid        string // compute fence (v0 rt.ComputeID)
	owner      string // v0 PrincipalID, Spec.Owner in v1
	space      string // v0 ContextID, Spec.Space in v1
	mode       string // v0 RuntimeMode ("gateway" only)
	address    string
}

// kubeObservation is the v0 ComputeObservation: state, compute id and the
// readiness-condition ladder the status surface projected.
type kubeObservation struct {
	state      string
	id         string
	address    string
	conditions []kubeCondition
}

type kubeCondition struct {
	name   string
	ok     bool
	since  time.Time
	reason string
}

const (
	kubeReasonReady                 = "ready"
	kubeReasonReadinessUnavailable  = "readiness_unavailable"
	kubeReasonNotChecked            = "not_checked"
)

func kubeHash(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }

// NewKubernetes validates the config exactly as v0 newKubernetesBackend
// did: relay origins, names, sizes and image-pull secrets fail closed, and
// without an injected client the API must be reached over verified TLS.
// The environment fallbacks (HUB_KUBERNETES_*) keep the v0 resolution order.
func NewKubernetes(cfg KubernetesConfig) (*Kubernetes, error) {
	if cfg.Namespace == "" {
		cfg.Namespace = envOr("HUB_KUBERNETES_NAMESPACE", "hermes-hub-"+cfg.Environment)
	}
	if cfg.RuntimeClassName == "" {
		cfg.RuntimeClassName = os.Getenv("HUB_RUNTIME_CLASS_NAME")
	}
	if cfg.RuntimeClassName == "" && cfg.ContainerRuntime != "runc" {
		cfg.RuntimeClassName = cfg.ContainerRuntime
	}
	if cfg.ContainerRuntime != "runc" && (cfg.RuntimeClassName == "runc" || cfg.RuntimeClassName == "runsc" || cfg.RuntimeClassName == "kata") && cfg.RuntimeClassName != cfg.ContainerRuntime {
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
		cfg.RelayImage = envOr("HUB_KUBERNETES_RELAY_IMAGE", cfg.ScriptImage)
	}
	if cfg.RelayImage == "" {
		cfg.RelayImage = cfg.Image
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
	if cfg.RuntimePort <= 0 {
		cfg.RuntimePort = 8080
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "2"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "2g"
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
	return &Kubernetes{cfg: cfg, client: client}, nil
}

// specWorkload derives the workload identity from the claim Spec (v1):
// owner+space replace the v0 principal/context pair; the pod name is the
// claim key; the generation rides the spec label set spawn stamps.
func (k Kubernetes) specWorkload(spec Spec) kubeWorkload {
	return kubeWorkload{
		name:       spec.Key,
		generation: spec.Labels["hermes-hub.generation"],
		owner:      spec.Owner,
		space:      spec.Space,
		mode:       "gateway", // the only mode the v0 pod plan admits
	}
}

func (k Kubernetes) scope(rt kubeWorkload) string {
	return hex.EncodeToString(kubeHash(rt.owner + "\x00" + rt.space))
}
func (k Kubernetes) annotations(rt kubeWorkload, generation bool) map[string]string {
	result := map[string]string{kubeOwner: k.cfg.OwnerID, kubeScope: k.scope(rt)}
	if generation {
		result[kubeGeneration] = rt.generation
		result[kubeContext] = hex.EncodeToString(kubeHash(rt.owner + "\x00" + rt.space + "\x00" + rt.mode))
	}
	return result
}
func (k Kubernetes) labels(rt kubeWorkload) map[string]string {
	return map[string]string{kubeOwner: k.cfg.OwnerID[:32], kubeScope: k.scope(rt)[:32], kubeContext: k.annotations(rt, true)[kubeContext][:32], "app.kubernetes.io/component": "runtime", "app.kubernetes.io/name": "hermes-hub", "app.kubernetes.io/instance": k.cfg.ControlRelease, kubeGeneration: rt.generation, "hermes-hub.io/environment": k.cfg.Environment}
}
func (k Kubernetes) claimName(rt kubeWorkload) string { return "space-" + k.scope(rt)[:32] }
func (k Kubernetes) secretName(rt kubeWorkload) string {
	return rt.name + "-" + hex.EncodeToString(kubeHash(rt.generation))[:12]
}

func (k Kubernetes) owned(object meta.Object, rt kubeWorkload, generation bool) bool {
	for key, value := range k.annotations(rt, generation) {
		if object.GetAnnotations()[key] != value {
			return false
		}
	}
	return rt.generation != "" && (!generation || rt.uid == "" || string(object.GetUID()) == rt.uid)
}

// ownerOnly is the weaker fence Inspect/Stop apply when only the workload
// name is known: the pod must carry this install's owner annotation. The
// scope+generation check (v0 owned) needs tenant identity the Backend
// Stop/Inspect signature does not carry — M40.15 restores it via the
// claim row.
func (k Kubernetes) ownerOnly(object meta.Object) bool {
	return object.GetAnnotations()[kubeOwner] == k.cfg.OwnerID && k.cfg.OwnerID != ""
}

func (k Kubernetes) pod(ctx context.Context, name string) (*core.Pod, error) {
	if k.client == nil {
		return nil, errors.New("kubernetes backend unconfigured")
	}
	pod, err := k.client.CoreV1().Pods(k.cfg.Namespace).Get(ctx, name, meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, errors.New("runtime Pod observation unavailable")
	}
	return pod, nil
}

// VerifyWorker re-checks a live pod's ownership and isolation — the v0
// Verify contract. A spec that names a different owner or generation is
// refused.
func (k Kubernetes) VerifyWorker(ctx context.Context, id string, spec Spec) error {
	pod, err := k.pod(ctx, id)
	if err != nil {
		return err
	}
	if !k.owned(pod, k.specWorkload(spec), true) {
		return errors.New("runtime Pod ownership mismatch")
	}
	if !k.podIsolationMatches(pod) {
		return errors.New("runtime Pod isolation mismatch")
	}
	return nil
}

func (k Kubernetes) podIsolationMatches(pod *core.Pod) bool {
	class := ""
	if pod.Spec.RuntimeClassName != nil {
		class = *pod.Spec.RuntimeClassName
	}
	return class == k.cfg.RuntimeClassName
}

func podObservation(pod *core.Pod, port int) kubeObservation {
	result := kubeObservation{id: string(pod.UID)}
	if pod.Status.PodIP != "" {
		result.address = "http://" + net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(port))
	}
	switch pod.Status.Phase {
	case core.PodPending:
		result.state = "created"
	case core.PodRunning:
		result.state = "running"
	case core.PodFailed, core.PodSucceeded:
		result.state = "exited"
	default:
		result.state = "unknown"
	}
	if pod.DeletionTimestamp != nil {
		result.state = "stopping"
	}
	for _, expected := range []struct {
		kind core.PodConditionType
		name string
	}{{core.PodScheduled, "pod_scheduled"}, {core.PodInitialized, "pod_initialized"}, {core.ContainersReady, "pod_containers_ready"}, {core.PodReady, "pod_ready"}} {
		condition := kubeCondition{name: expected.name, reason: kubeReasonNotChecked}
		for _, observed := range pod.Status.Conditions {
			if observed.Type == expected.kind {
				condition.ok = observed.Status == core.ConditionTrue
				condition.since = observed.LastTransitionTime.Time
				condition.reason = kubeReasonReadinessUnavailable
				if condition.ok {
					condition.reason = kubeReasonReady
				}
			}
		}
		result.conditions = append(result.conditions, condition)
	}
	return result
}

// relayRuntime names the companion relay pod for one workload (v0).
func (k Kubernetes) relayName(rt kubeWorkload) string { return rt.name + "-relay" }
func (k Kubernetes) relaySecretName(rt kubeWorkload) string { return k.secretName(rt) + "-relay" }

func (k Kubernetes) observe(ctx context.Context, rt kubeWorkload) (kubeObservation, error) {
	pod, err := k.pod(ctx, rt.name)
	if err != nil {
		return kubeObservation{}, err
	}
	if !k.owned(pod, rt, true) {
		return kubeObservation{}, errors.New("runtime Pod ownership mismatch")
	}
	if !k.podIsolationMatches(pod) {
		return kubeObservation{}, errors.New("runtime Pod isolation mismatch")
	}
	result := podObservation(pod, k.cfg.RuntimePort)
	relayRT := k.relayWorkload(rt)
	relay, relayErr := k.pod(ctx, relayRT.name)
	condition := kubeCondition{name: "relays_ready", reason: kubeReasonReadinessUnavailable}
	if relayErr == nil && k.owned(relay, relayRT, true) && relay.Annotations[kubeRole] == "relay" {
		for _, observed := range podObservation(relay, k.cfg.RuntimePort).conditions {
			if observed.name == "pod_ready" {
				condition.ok, condition.since = observed.ok && relay.DeletionTimestamp == nil, observed.since
			}
		}
	}
	if condition.ok {
		condition.reason = kubeReasonReady
	}
	result.conditions = append(result.conditions, condition)
	return result, nil
}

// Inspect maps the pod observation to the backend Runtime surface; the
// owner annotation is the only fence the bare id can prove (see ownerOnly).
func (k Kubernetes) Inspect(ctx context.Context, id string) (Runtime, error) {
	pod, err := k.pod(ctx, id)
	if err != nil {
		return Runtime{}, err
	}
	if !k.ownerOnly(pod) {
		return Runtime{}, errors.New("runtime Pod ownership mismatch")
	}
	if !k.podIsolationMatches(pod) {
		return Runtime{}, errors.New("runtime Pod isolation mismatch")
	}
	obs := podObservation(pod, k.cfg.RuntimePort)
	return Runtime{ID: obs.id, Name: pod.Name, State: obs.state, Address: obs.address}, nil
}

// Start runs the v0 Spawn sequence: ownership pre-check, pod+secret plan,
// PVC and NetworkPolicy admission, relay first, then the runtime pod
// polled to running — all inside one bounded context.
func (k Kubernetes) Start(ctx context.Context, spec Spec) (Runtime, error) {
	if !spec.Managed() {
		return Runtime{}, errors.New("kubernetes requires the managed runtime contract")
	}
	rt := k.specWorkload(spec)
	dir, err := specSpaceDir(k.cfg.SpacesRoot, spec.Space)
	if err != nil {
		return Runtime{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	for _, name := range []string{rt.name, k.relayName(rt)} {
		if existing, err := k.pod(ctx, name); err == nil {
			expectedRole := ""
			if name != rt.name {
				expectedRole = "relay"
			}
			probe := rt
			probe.name = name
			if !k.owned(existing, probe, true) || existing.Annotations[kubeRole] != expectedRole {
				return Runtime{}, errors.New("runtime Pod ownership mismatch")
			}
		} else if !errors.Is(err, ErrNotFound) {
			return Runtime{}, err
		}
	}
	main, secret, err := k.plan(spec, rt, dir)
	if err != nil {
		return Runtime{}, err
	}
	relay, relaySecret, err := k.relayPlan(spec, rt, main, secret, dir)
	if err != nil {
		return Runtime{}, err
	}
	if err = k.ensureStorage(ctx, rt); err != nil {
		return Runtime{}, err
	}
	if err = k.ensurePolicy(ctx, rt); err != nil {
		return Runtime{}, err
	}
	if err = k.createSecret(ctx, relaySecret, rt); err != nil {
		return Runtime{}, err
	}
	readyRelay, err := k.startRelay(ctx, relay, rt)
	if err != nil {
		return Runtime{}, err
	}
	if err = k.bindPolicyReceipt(ctx, spec, rt, main, secret, readyRelay.Status.PodIP); err != nil {
		return Runtime{}, err
	}
	if err = k.createSecret(ctx, secret, rt); err != nil {
		return Runtime{}, err
	}
	if _, err = k.client.CoreV1().Pods(k.cfg.Namespace).Create(ctx, main, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		if k.cfg.RuntimeClassName != "" {
			return Runtime{}, errors.New("runtime Pod admission failed; verify selected RuntimeClass and namespace policy")
		}
		return Runtime{}, errors.New("runtime Pod creation failed")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		observation, err := k.observe(ctx, rt)
		if err != nil {
			return Runtime{}, err
		}
		if observation.state == "exited" {
			return Runtime{}, errors.New("runtime Pod exited during spawn")
		}
		if observation.address != "" && observation.state == "running" {
			return Runtime{ID: observation.id, Name: rt.name, Space: spec.Space, Agent: spec.Agent, State: "running", Address: observation.address}, nil
		}
		select {
		case <-ctx.Done():
			return Runtime{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k Kubernetes) ensureStorage(ctx context.Context, rt kubeWorkload) error {
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

func (k Kubernetes) peer(component string) networking.NetworkPolicyPeer {
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

func (k Kubernetes) policy(rt kubeWorkload) *networking.NetworkPolicy {
	selector := k.labels(rt)
	annotations := k.annotations(rt, false)
	annotations[kubeContext] = k.annotations(rt, true)[kubeContext]
	policy := &networking.NetworkPolicy{ObjectMeta: meta.ObjectMeta{Name: rt.name, Annotations: annotations, Labels: k.labels(rt)}, Spec: networking.NetworkPolicySpec{PodSelector: meta.LabelSelector{MatchLabels: selector}, PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}, Ingress: []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{k.peer("supervisor")}, Ports: []networking.NetworkPolicyPort{kubePort(int32(k.cfg.RuntimePort), core.ProtocolTCP)}}}}} // #nosec G115 -- runtime port is normalized to 1-65535 in config
	policy.Spec.Egress = []networking.NetworkPolicyEgressRule{{To: []networking.NetworkPolicyPeer{{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, PodSelector: &meta.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}}}}, Ports: []networking.NetworkPolicyPort{kubePort(53, core.ProtocolUDP), kubePort(53, core.ProtocolTCP)}}}
	relay := k.labels(rt)
	relay["app.kubernetes.io/component"] = "runtime-relay"
	policy.Spec.Egress = append(policy.Spec.Egress, networking.NetworkPolicyEgressRule{To: []networking.NetworkPolicyPeer{{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": k.cfg.Namespace}}, PodSelector: &meta.LabelSelector{MatchLabels: relay}}}, Ports: []networking.NetworkPolicyPort{kubePort(8090, core.ProtocolTCP), kubePort(8318, core.ProtocolTCP), kubePort(8319, core.ProtocolTCP)}})
	return policy
}

func (k Kubernetes) ensurePolicy(ctx context.Context, rt kubeWorkload) error {
	guard := &networking.NetworkPolicy{ObjectMeta: meta.ObjectMeta{Name: k.claimName(rt) + "-guard", Annotations: k.annotations(rt, false)}, Spec: networking.NetworkPolicySpec{PodSelector: meta.LabelSelector{MatchLabels: map[string]string{kubeOwner: k.cfg.OwnerID[:32], kubeScope: k.scope(rt)[:32]}}, PolicyTypes: []networking.PolicyType{networking.PolicyTypeIngress, networking.PolicyTypeEgress}}}
	for _, wanted := range []*networking.NetworkPolicy{guard, k.policy(rt), k.relayPolicy(rt)} {
		if err := k.ensurePolicyObject(ctx, rt, wanted); err != nil {
			return err
		}
	}
	return nil
}
func (k Kubernetes) ensurePolicyObject(ctx context.Context, rt kubeWorkload, wanted *networking.NetworkPolicy) error {
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

// Stop removes the runtime and relay pods plus their generation Secrets
// (v0 Remove). The workload re-derives its generation/UID fence from the
// pod's own annotations — owner annotation plus name is the provable
// fence; M40.15 restores the scope check once the claim row supplies it.
func (k Kubernetes) Stop(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, name := range []string{id, id + "-relay"} {
		if err := k.removePod(ctx, name); err != nil {
			return err
		}
	}
	// Generation secrets are <name>-<hash12> and <name>-<hash12>-relay; the
	// hash is not recoverable from the id alone, so a label-bounded list
	// plus the owner annotation gates the sweep.
	secrets, err := k.client.CoreV1().Secrets(k.cfg.Namespace).List(ctx, meta.ListOptions{LabelSelector: kubeOwner + "=" + k.cfg.OwnerID[:32]})
	if err != nil {
		return errors.New("runtime Secret cleanup unavailable")
	}
	for _, secret := range secrets.Items {
		base := strings.TrimSuffix(secret.Name, "-relay")
		if !strings.HasPrefix(base, id+"-") || len(base) != len(id)+13 {
			continue
		}
		if secret.Annotations[kubeOwner] != k.cfg.OwnerID {
			continue
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

func (k Kubernetes) removePod(ctx context.Context, name string) error {
	pod, err := k.pod(ctx, name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !k.ownerOnly(pod) {
		return errors.New("runtime Pod ownership mismatch")
	}
	if err = k.client.CoreV1().Pods(k.cfg.Namespace).Delete(ctx, pod.Name, deleteUID(pod.UID)); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("runtime Pod deletion failed")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := k.pod(ctx, name)
		if errors.Is(err, ErrNotFound) {
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

// List inventories only this install's runtime pods (v0 inventory): relay
// pods and foreign-owner pods never surface; malformed ownership evidence
// fails closed.
func (k Kubernetes) List(ctx context.Context) ([]Runtime, error) {
	pods, err := k.client.CoreV1().Pods(k.cfg.Namespace).List(ctx, meta.ListOptions{LabelSelector: kubeOwner + "=" + k.cfg.OwnerID[:32], Limit: 257})
	if err != nil || len(pods.Items) > 256 || pods.Continue != "" {
		return nil, errors.New("runtime Pod inventory unavailable or over limit")
	}
	var result []Runtime
	for _, pod := range pods.Items {
		if pod.Annotations[kubeRole] == "relay" {
			continue
		}
		if pod.Annotations[kubeOwner] != k.cfg.OwnerID {
			continue
		}
		if len(pod.Annotations[kubeContext]) != 64 || pod.Annotations[kubeGeneration] == "" {
			return nil, errors.New("invalid runtime Pod inventory ownership")
		}
		obs := podObservation(&pod, k.cfg.RuntimePort)
		result = append(result, Runtime{ID: string(pod.UID), Name: pod.Name, State: obs.state, Address: obs.address})
	}
	return result, nil
}

// Exec is unsupported on the pod backend: v0 had no cell/exec surface for
// Kubernetes and M40.15 decides whether workers exist there at all.
func (k Kubernetes) Exec(context.Context, string, ExecSpec) (ExecResult, error) {
	return ExecResult{}, errors.New("kubernetes backend does not exec")
}

// specSpaceDir resolves the space's host root for the input projection —
// the v0 binding.ContextRoot, now SpacesRoot/space under the controller.
func specSpaceDir(spacesRoot, space string) (string, error) {
	if spacesRoot == "" || space == "" {
		return "", errors.New("kubernetes requires a spaces root and space")
	}
	return filepath.Join(spacesRoot, space), nil
}
