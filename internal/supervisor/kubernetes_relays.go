package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	taskcontract "github.com/letya999/hermes-hub/contracts/task/v1"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	core "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (k *kubernetesBackend) relayRuntime(rt Runtime) Runtime {
	rt.Container += "-relay"
	rt.ComputeID = ""
	return rt
}
func (k *kubernetesBackend) relaySecretName(rt Runtime) string { return k.secretName(rt) + "-relay" }

func relayFile(root, name string, required bool) (map[string]string, error) {
	if name != filepath.Base(name) {
		return nil, errors.New("relay environment source unavailable")
	}
	info, err := os.Lstat(filepath.Join(root, name)) // #nosec G703 -- name is restricted to a base filename above; root is the binding context directory
	if os.IsNotExist(err) && !required {
		return map[string]string{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > kubeInputBudget {
		return nil, errors.New("relay environment source unavailable")
	}
	values, err := stack.ReadSecrets(filepath.Join(root, name))
	if err != nil {
		return nil, errors.New("relay environment invalid")
	}
	return values, nil
}

func relayEnvironment(secret *core.Secret, values map[string]string, prefix string) ([]core.EnvVar, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 512 {
		return nil, errors.New("relay environment entry limit exceeded")
	}
	var result []core.EnvVar
	for i, key := range keys {
		if !kubeEnvName.MatchString(key) || strings.ContainsRune(values[key], 0) {
			return nil, errors.New("invalid relay environment entry")
		}
		field := fmt.Sprintf("%s%04d", prefix, i)
		secret.Data[field] = []byte(values[key])
		result = append(result, core.EnvVar{Name: key, ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: secret.Name}, Key: field}}})
	}
	size := 0
	for _, value := range secret.Data {
		size += len(value)
	}
	if size > kubeInputBudget {
		return nil, errors.New("relay input byte limit exceeded")
	}
	return result, nil
}

func (k *kubernetesBackend) relayPlan(request SpawnRequest, main *core.Pod, inputs *core.Secret) (*core.Pod, *core.Secret, error) {
	if !request.Runtime.Managed {
		return nil, nil, errors.New("kubernetes requires the managed runtime contract")
	}
	relay := main.DeepCopy()
	relay.Name = k.relayRuntime(request.Runtime).Container
	relay.Annotations[kubeRole] = "relay"
	relay.Labels["app.kubernetes.io/component"] = "runtime-relay"
	relay.Spec.RuntimeClassName = nil
	relay.Spec.HostAliases = nil
	secret := inputs.DeepCopy()
	secret.Name = k.relaySecretName(request.Runtime)
	secret.Annotations[kubeRole] = "relay"
	for i := range relay.Spec.Volumes {
		if relay.Spec.Volumes[i].Secret != nil {
			relay.Spec.Volumes[i].Secret.SecretName = secret.Name
		}
	}
	// The relay does no PVC or projection preparation: every mount it needs is
	// either a whole-volume PVC alias or a direct Secret projection.
	relay.Spec.InitContainers = nil
	values := map[string]string{}
	for _, name := range []string{"runtime." + k.m.environment() + ".env", "model-relay." + k.m.environment() + ".env", "model-relay.auth"} {
		entries, err := relayFile(request.Binding.ContextRoot, name, name == "model-relay.auth")
		if err != nil {
			return nil, nil, err
		}
		for key, value := range entries {
			values[key] = value
		}
	}
	modelState := persistPath(false, "managed/"+k.m.environment()+"/model-relay") + "/model-select.json"
	// The private relay keeps its existing PDP and registry contracts. No
	// control bearer or provider credential is projected into the runtime.
	release := k.cfg.ControlRelease + "-hermes-hub"
	verificationURL := "http://" + strings.TrimSuffix(release[:min(len(release), 45)], "-") + "-supervisor." + k.cfg.ControlNamespace + ".svc:8876"
	for key, value := range map[string]string{
		"HUB_MODEL_RELAY_REGISTRY": "/run/model-config/models.yaml", "HUB_MODEL_RELAY_STATE": modelState,
		"HUB_MODEL_RELAY_OWNER": request.Binding.UserID, "HUB_MODEL_RELAY_CLIPROXY": k.cfg.CliproxyURL,
		"HUB_TOOLHUB_URL": k.cfg.ToolHubURL, "HUB_TOOLHUB_CONTROL": request.Binding.runtimeAuth, "HUB_ENVIRONMENT": k.m.environment(),
		"HUB_RUN_VERIFY_URL": verificationURL, "HUB_RUN_VERIFY_AUTH": taskcontract.VerificationCredential(k.m.cfg.RuntimeAuth),
	} {
		values[key] = value
	}
	modelEnv, err := relayEnvironment(secret, values, "r")
	if err != nil {
		return nil, nil, err
	}
	toolEnv, err := relayEnvironment(secret, map[string]string{"HUB_TOOLHUB_RELAY_UPSTREAM": k.cfg.ToolHubURL}, "t")
	if err != nil {
		return nil, nil, err
	}
	var modelMounts []core.VolumeMount
	for _, mount := range main.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/run/model-config" {
			modelMounts = append(modelMounts, mount)
		}
	}
	if len(modelMounts) != 1 {
		return nil, nil, errors.New("model relay registry mount unavailable")
	}
	modelMounts = append(modelMounts, core.VolumeMount{Name: "space", MountPath: kubePersistRoot}, core.VolumeMount{Name: "tmp", MountPath: "/tmp"})
	if values["HUB_CREDENTIAL_BROKER_MODEL_PROFILE"] != "" || values["HUB_CREDENTIAL_BROKER_MODEL_KEY_FILE"] != "" {
		mode := int32(0440)
		relay.Spec.Volumes = append(relay.Spec.Volumes, core.Volume{Name: "broker-profile", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: k.claimName(request.Runtime) + "-broker", DefaultMode: &mode}}})
		modelMounts = append(modelMounts, core.VolumeMount{Name: "broker-profile", MountPath: "/run/broker-secrets", ReadOnly: true})
	}
	base := main.Spec.Containers[0].DeepCopy()
	base.Image = k.cfg.RelayImage
	base.Args = nil
	base.Env = nil
	base.VolumeMounts = nil
	probe := func(port int) *core.Probe {
		return &core.Probe{ProbeHandler: core.ProbeHandler{TCPSocket: &core.TCPSocketAction{Port: intstr.FromInt(port)}}, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 3}
	}
	model := base.DeepCopy()
	model.Name = "model-relay"
	model.Command = []string{"hub-runtime", "model-relay"}
	model.Env = modelEnv
	model.VolumeMounts = modelMounts
	model.ReadinessProbe = probe(8318)
	tool := base.DeepCopy()
	tool.Name = "toolhub-relay"
	tool.Command = []string{"hub-runtime", "toolhub-relay"}
	tool.Env = toolEnv
	tool.VolumeMounts = []core.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
	tool.ReadinessProbe = probe(8090)
	relay.Spec.Containers = []core.Container{*model, *tool}
	settings := request.Settings
	settings.Environment = k.m.environment()
	if environment := stack.RuntimeEgressEnvironment(settings, request.Binding.ContextRoot); environment != nil {
		environment["HUB_EGRESS_RELAY_OWNER"] = request.Binding.UserID
		environment["HUB_ENVIRONMENT"] = k.m.environment()
		environment["HUB_RUN_VERIFY_URL"] = verificationURL
		environment["HUB_RUN_VERIFY_AUTH"] = taskcontract.VerificationCredential(k.m.cfg.RuntimeAuth)
		environment["HUB_TOOLHUB_URL"] = k.cfg.ToolHubURL
		env, err := relayEnvironment(secret, environment, "x")
		if err != nil {
			return nil, nil, err
		}
		egress := base.DeepCopy()
		egress.Name = "egress-relay"
		egress.Command = []string{"hub-runtime", "egress-relay"}
		egress.Env = env
		egress.VolumeMounts = []core.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}
		egress.ReadinessProbe = probe(8319)
		relay.Spec.Containers = append(relay.Spec.Containers, *egress)
	}
	// Secret volumes mounted by relay containers project Secret keys that live
	// in the runtime Secret. Copy only the keys referenced by volumes the relay
	// actually mounts into the relay Secret, or kubelet fails the mount.
	mounted := map[string]bool{}
	for _, container := range relay.Spec.Containers {
		for _, vm := range container.VolumeMounts {
			mounted[vm.Name] = true
		}
	}
	for i := range relay.Spec.Volumes {
		volume := &relay.Spec.Volumes[i]
		if volume.Secret == nil || !mounted[volume.Name] {
			continue
		}
		for _, item := range volume.Secret.Items {
			if data, ok := inputs.Data[item.Key]; ok {
				secret.Data[item.Key] = data
			}
		}
	}
	// RWO claims may be shared only on one node. The first relay can schedule
	// freely; the runtime follows that exact relay generation.
	selector := k.labels(request.Runtime)
	selector["app.kubernetes.io/component"] = "runtime-relay"
	main.Spec.Affinity = &core.Affinity{PodAffinity: &core.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []core.PodAffinityTerm{{TopologyKey: "kubernetes.io/hostname", LabelSelector: &meta.LabelSelector{MatchLabels: selector}}}}}
	relay.Spec.Affinity = &core.Affinity{PodAffinity: &core.PodAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []core.PodAffinityTerm{{TopologyKey: "kubernetes.io/hostname", LabelSelector: &meta.LabelSelector{MatchLabels: map[string]string{kubeOwner: k.m.ownerID()[:32], kubeScope: k.scope(request.Runtime)[:32], "app.kubernetes.io/component": "runtime-relay"}}}}}}
	return relay, secret, nil
}

func (k *kubernetesBackend) createSecret(ctx context.Context, secret *core.Secret, rt Runtime) error {
	client := k.client.CoreV1().Secrets(k.cfg.Namespace)
	if _, err := client.Create(ctx, secret, meta.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errors.New("runtime Secret creation failed")
		}
		existing, err := client.Get(ctx, secret.Name, meta.GetOptions{})
		if err != nil || !k.secretMatches(existing, secret, rt) {
			return errors.New("runtime Secret ownership or content mismatch")
		}
	}
	return nil
}

func (k *kubernetesBackend) startRelay(ctx context.Context, pod *core.Pod, rt Runtime) (*core.Pod, error) {
	if _, err := k.client.CoreV1().Pods(k.cfg.Namespace).Create(ctx, pod, meta.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, errors.New("private relay Pod creation failed")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := k.pod(ctx, k.relayRuntime(rt))
		if err != nil {
			return nil, err
		}
		if !k.owned(current, k.relayRuntime(rt), true) || current.Annotations[kubeRole] != "relay" {
			return nil, errors.New("private relay Pod ownership mismatch")
		}
		if current.Status.Phase == core.PodFailed {
			return nil, errors.New("private relay Pod failed")
		}
		if current.Status.PodIP != "" {
			for _, condition := range current.Status.Conditions {
				if condition.Type == core.PodReady && condition.Status == core.ConditionTrue {
					return current, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *kubernetesBackend) bindPolicyReceipt(ctx context.Context, pod *core.Pod, secret *core.Secret, rt Runtime, relayIP string) error {
	policy, err := k.client.NetworkingV1().NetworkPolicies(k.cfg.Namespace).Get(ctx, rt.Container, meta.GetOptions{})
	if err != nil || policy.UID == "" {
		return errors.New("runtime NetworkPolicy receipt unavailable")
	}
	encoded, err := json.Marshal(policy.Spec)
	if err != nil {
		return errors.New("runtime policy encoding failed")
	}
	receipt := hubruntime.KubernetesIsolationReceipt{Namespace: k.cfg.Namespace, Pod: pod.Name, Generation: rt.Generation, ContextHash: k.annotations(rt, true)[kubeContext], PolicyUID: string(policy.UID), PolicySHA256: hex.EncodeToString(hashBytes(string(encoded))), RelayIP: relayIP}
	body, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("runtime policy receipt encoding failed")
	}
	secret.Data["network-policy"] = body
	mode := int32(0440)
	pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: "isolation", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: secret.Name, DefaultMode: &mode, Items: []core.KeyToPath{{Key: "network-policy", Path: "policy.json"}}}}})
	pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, core.VolumeMount{Name: "isolation", MountPath: "/run/hub-isolation", ReadOnly: true})
	pod.Spec.HostAliases = []core.HostAlias{{IP: relayIP, Hostnames: []string{"toolhub", "model-relay", "egress-relay"}}}
	hermesHome := kubePersistRoot + "/managed/" + k.m.environment() + "/hermes"
	for _, name := range managedExtensionRootTmpfs {
		volume := "extension-" + name
		pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: volume, VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{Medium: core.StorageMediumMemory}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, core.VolumeMount{Name: volume, MountPath: hermesHome + "/" + name, ReadOnly: true})
	}
	return nil
}

func (k *kubernetesBackend) relayPolicy(rt Runtime) *networking.NetworkPolicy {
	policy := k.policy(rt)
	policy.Name = rt.Container + "-relay"
	policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] = "runtime-relay"
	peer := networking.NetworkPolicyPeer{NamespaceSelector: &meta.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": k.cfg.Namespace}}, PodSelector: &meta.LabelSelector{MatchLabels: k.labels(rt)}}
	policy.Spec.Ingress = []networking.NetworkPolicyIngressRule{{From: []networking.NetworkPolicyPeer{peer}, Ports: []networking.NetworkPolicyPort{kubePort(8090, core.ProtocolTCP), kubePort(8318, core.ProtocolTCP), kubePort(8319, core.ProtocolTCP)}}}
	// DNS is the first egress rule; relays alone may reach reviewed control peers.
	policy.Spec.Egress = policy.Spec.Egress[:1]
	for _, target := range []struct {
		name string
		port int32
	}{{"toolhub", 8090}, {"cliproxy", 8317}, {"credential-broker", 8787}, {"supervisor", 8876}} {
		policy.Spec.Egress = append(policy.Spec.Egress, networking.NetworkPolicyEgressRule{To: []networking.NetworkPolicyPeer{k.peer(target.name)}, Ports: []networking.NetworkPolicyPort{kubePort(target.port, core.ProtocolTCP)}})
	}
	return policy
}
