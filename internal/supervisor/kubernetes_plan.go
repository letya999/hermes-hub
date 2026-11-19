package supervisor

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const kubeInputBudget = 512 * 1024

// kubeRoViewBudget bounds a read-only workspace subtree snapshot copied into
// a dedicated memory volume so nested read-only binds keep kernel enforcement
// without subPath.
const kubeRoViewBudget = 64 * 1024 * 1024

// Kata guests cannot see subPath mounts, so the whole contract resolves
// through whole-volume mounts: the space PVC lands at kubePersistRoot and a
// read-only alias at kubePersistRoRoot, generated inputs project into Secret
// volume groups, and the memory-backed private staging tree is shared whole
// between init and runtime containers.
const (
	kubePersistRoot   = "/persist"
	kubePersistRoRoot = "/persist-ro"
	kubeInputsRoot    = "/run/inputs"
	kubePrivateRoot   = "/private"
	kubeSharedRoot    = "/shared"
	kubeHermesConfDir = "hermes-config"
)

// kubeEnvDirTargets are the directory targets whose consumers resolve them
// through environment variables, so a whole-PVC mount can serve the same
// content at the env-pointed path.
var kubeEnvDirTargets = map[string]bool{
	"/state": true, "/state/hermes": true, "/state/home": true, "/state/cache": true,
	"/workspace": true, "/vault": true, "/state/ssh": true, "/state/exposure": true,
	"/run/model-state": true, "/skills-store": true, "/skills-store-org": true,
}

// kubeEnvFileTargets are file targets consumed through an env-pointed path.
var kubeEnvFileTargets = map[string]bool{"/run/hosting.key": true}

// kubeGeneratedDirTargets are generated directory targets: /config owns a
// dedicated Secret volume, the remaining entries are env-pointed.
var kubeGeneratedDirTargets = map[string]bool{
	"/config": true, "/skills": true, "/skills-platform": true, "/skills-meta": true, "/skills-effective-rw": true,
}

var kubeEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var kubeRoViewLimit = resource.MustParse("64Mi")

type kubeInputs struct {
	root   *os.Root
	data   map[string][]byte
	groups map[string][]core.KeyToPath
	size   int
	count  int
}

func (in *kubeInputs) addFile(source, target, group string) error {
	if in.count >= 512 {
		return errors.New("runtime input file limit exceeded")
	}
	file, err := in.root.Open(filepath.FromSlash(source))
	if err != nil {
		return errors.New("runtime input unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("runtime input is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(kubeInputBudget-in.size)+1))
	if err != nil || len(data) > kubeInputBudget-in.size {
		return errors.New("runtime input byte limit exceeded")
	}
	key := fmt.Sprintf("f%04d", in.count)
	in.count++
	in.size += len(data)
	in.data[key] = data
	if in.groups == nil {
		in.groups = map[string][]core.KeyToPath{}
	}
	in.groups[group] = append(in.groups[group], core.KeyToPath{Key: key, Path: target})
	return nil
}

func (in *kubeInputs) snapshot(source, target, group string) error {
	return fs.WalkDir(in.root.FS(), source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("runtime input tree unavailable or contains symlinks")
		}
		if strings.Count(name, "/") > 20 {
			return errors.New("runtime input depth exceeded")
		}
		if entry.IsDir() {
			return nil
		}
		suffix := strings.TrimPrefix(strings.TrimPrefix(name, source), "/")
		projected := suffix
		if target != "" {
			projected = target + "/" + suffix
		}
		return in.addFile(name, projected, group)
	})
}

func kubeTreeSize(fsys fs.FS, dir string) (int64, error) {
	var total int64
	err := fs.WalkDir(fsys, dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("runtime input tree unavailable or contains symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("runtime input is not a regular file")
		}
		total += info.Size()
		if total > kubeRoViewBudget {
			return errors.New("runtime input staging budget exceeded")
		}
		return nil
	})
	return total, err
}

func persistPath(readOnly bool, rel string) string {
	if readOnly {
		return kubePersistRoRoot + "/" + rel
	}
	return kubePersistRoot + "/" + rel
}

func spaceRelative(root, source string) (string, error) {
	rel, err := filepath.Rel(root, filepath.FromSlash(source))
	if err != nil || rel == "." || !fs.ValidPath(filepath.ToSlash(rel)) || strings.ContainsAny(rel, "\r\n\x00") {
		return "", errors.New("runtime mount escapes its space")
	}
	return filepath.ToSlash(rel), nil
}

func (k *kubernetesBackend) secretMatches(current, wanted *core.Secret, rt Runtime) bool {
	rt.ComputeID = ""
	return current != nil && k.owned(current, rt, true) && current.Immutable != nil && *current.Immutable && reflect.DeepEqual(current.Data, wanted.Data)
}

func (k *kubernetesBackend) plan(request SpawnRequest) (*core.Pod, *core.Secret, error) {
	binding, settings, rt := request.Binding, request.Settings, request.Runtime
	// shortcut: one gateway per space keeps relay state single-writer; extend with shared relays before adding modes.
	if binding.RuntimeMode != "gateway" {
		return nil, nil, errors.New("kubernetes supports the gateway runtime mode")
	}
	if settings.PassportRevision == "" {
		return nil, nil, errors.New("kubernetes runtime requires a published space revision")
	}
	if settings.Has("browser") || settings.Has("meet") {
		return nil, nil, errors.New("kubernetes browser sidecar is not configured")
	}
	root, err := os.OpenRoot(binding.ContextRoot)
	if err != nil {
		return nil, nil, errors.New("space input root unavailable")
	}
	defer root.Close()
	// Materializers write only to these control-owned directories. Refuse symlink
	// parents before invoking the existing publication helpers.
	for _, dir := range []string{"generated", "skills", "managed"} {
		if info, e := root.Lstat(dir); e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return nil, nil, errors.New("space materialization root is not an owned directory")
		}
	}
	settings.Environment = k.m.environment()
	if settings.GlobalSkillsDir == "" {
		settings.DefaultGlobalSkillsDir(filepath.Join(filepath.Dir(filepath.Dir(binding.ContextRoot)), "config", "skills"))
	}
	if err := os.MkdirAll(settings.GlobalSkillsDir, 0755); err != nil { // #nosec G301 -- shared public platform skill inputs, never credential material
		return nil, nil, errors.New("platform skills projection unavailable")
	}
	if _, err := k.m.materializeHermesConfig(binding, settings.Environment); err != nil {
		return nil, nil, errors.New("runtime Hermes configuration unavailable")
	}
	if _, err := stack.MaterializeEffectiveSkills(binding.ContextRoot, settings); err != nil {
		return nil, nil, errors.New("runtime skills projection unavailable")
	}
	if _, err := stack.MaterializeToolPolicySnapshot(binding.ContextRoot, settings); err != nil {
		return nil, nil, errors.New("runtime policy projection unavailable")
	}
	if err := stack.MaterializeModelRegistry(binding.ContextRoot, settings); err != nil {
		return nil, nil, errors.New("runtime model registry projection unavailable")
	}
	if err := stack.EnsureModelRelayAuth(binding.ContextRoot); err != nil {
		return nil, nil, errors.New("model relay credentials unavailable")
	}
	service := stack.RuntimeService(settings, "", binding.ContextRoot)
	in := &kubeInputs{root: root, data: map[string][]byte{}}
	values := map[string]string{}
	envName := "runtime." + settings.Environment + ".env"
	if rt.Managed {
		envName = "managed-runtime." + settings.Environment + ".env"
	}
	for _, name := range []string{envName, filepath.Base(binding.EnvFile)} {
		info, e := root.Lstat(name)
		if e != nil || !info.Mode().IsRegular() || info.Size() > kubeInputBudget {
			return nil, nil, errors.New("runtime environment source unavailable")
		}
		// The existing parser owns dotenv semantics. Never include its contents or
		// dictionary keys in errors or diagnostics.
		entries, e := stack.ReadSecrets(filepath.Join(binding.ContextRoot, name))
		if e != nil {
			return nil, nil, errors.New("runtime environment unavailable")
		}
		for key, value := range entries {
			if name == filepath.Base(binding.EnvFile) && key != "HUB_RUNTIME_AUTH" && key != "API_SERVER_KEY" && key != "HUB_TOOLHUB_AUTH" {
				continue // Legacy provider/control entries never belong in a managed runtime.
			}
			values[key] = value
		}
	}
	for key, value := range service["environment"].(stack.M) {
		values[key] = fmt.Sprint(value)
	}
	if rt.Managed {
		auth, err := stack.ReadSecrets(filepath.Join(binding.ContextRoot, "model-relay.auth"))
		if err != nil || auth["HUB_MODEL_RELAY_AUTH"] == "" {
			return nil, nil, errors.New("model relay caller credential unavailable")
		}
		values["OPENAI_API_KEY"], values["OPENAI_BASE_URL"] = auth["HUB_MODEL_RELAY_AUTH"], "http://model-relay:8318/v1"
	}
	values["HUB_EXECUTION_SUBSTRATE"], values["HUB_RUNTIME_MODE"] = "kubernetes", binding.RuntimeMode
	delete(values, "HUB_KUBERNETES_POD_NAME")
	delete(values, "HUB_KUBERNETES_POD_NAMESPACE")
	for key, value := range map[string]string{"HUB_RUNTIME_LISTEN": "0.0.0.0:" + strconv.Itoa(k.m.cfg.RuntimePort), "HUB_RUNTIME_AUTH": binding.runtimeAuth, "API_SERVER_KEY": binding.runtimeAuth, "HUB_USER_ID": binding.UserID, "HUB_PRINCIPAL_ID": binding.PrincipalID, "HUB_CONTEXT_ID": binding.ContextID, "HUB_RUNTIME_ID": binding.RuntimeID, "HUB_ORGANIZATION_ID": binding.OrganizationID, "HUB_POLICY_VERSION": binding.PolicyVersion, "HUB_RUNTIME_GENERATION": rt.Generation, "API_SERVER_ENABLED": "true", "API_SERVER_HOST": "127.0.0.1", "API_SERVER_PORT": "8642"} {
		values[key] = value
	}
	// Resolve mounts before encoding environment: env-pointed paths are
	// rewritten to the whole-volume roots so consumers keep working without
	// per-target mounts.
	volumes := []core.Volume{{Name: "space", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: k.claimName(rt)}}}, {Name: "private", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{Medium: core.StorageMediumMemory}}}, {Name: "tmp", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{Medium: core.StorageMediumMemory}}}}
	var directories []string
	var privateCopies, pvcLinks, sharedLinks, roViews []string
	var redirects []struct{ from, to string }
	hermesRel := ""
	snapshots := map[string]string{}
	for i, raw := range service["volumes"].([]any) {
		mount, ok := raw.(stack.M)
		if !ok || mount["type"] != "bind" {
			return nil, nil, errors.New("kubernetes requires space-scoped bind inputs")
		}
		source, ok := mount["source"].(string)
		if !ok {
			return nil, nil, errors.New("invalid runtime mount source")
		}
		target, ok := mount["target"].(string)
		if !ok || !strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\r\n\x00") {
			return nil, nil, errors.New("invalid runtime mount target")
		}
		rel, e := spaceRelative(binding.ContextRoot, source)
		if e != nil {
			return nil, nil, e
		}
		ro, _ := mount["read_only"].(bool)
		if strings.HasPrefix(rel, "generated/") {
			info, e := fs.Stat(root.FS(), rel)
			if e != nil {
				return nil, nil, fmt.Errorf("runtime mount source unavailable: %s", rel)
			}
			if info.IsDir() {
				if !kubeGeneratedDirTargets[target] {
					return nil, nil, fmt.Errorf("unsupported generated mount target %q", target)
				}
				if target == "/config" {
					if e := in.snapshot(rel, "", "cfg"); e != nil {
						return nil, nil, fmt.Errorf("runtime input projection %d: %w", i, e)
					}
					continue
				}
				proj, exists := snapshots[rel]
				if !exists {
					proj = fmt.Sprintf("m%04d", i)
					if e := in.snapshot(rel, proj, "inputs"); e != nil {
						return nil, nil, fmt.Errorf("runtime input projection %d: %w", i, e)
					}
					snapshots[rel] = proj
				}
				if ro {
					redirects = append(redirects, struct{ from, to string }{target, kubeInputsRoot + "/" + proj})
				} else {
					privateCopies = append(privateCopies, proj)
					redirects = append(redirects, struct{ from, to string }{target, kubePrivateRoot + "/" + proj})
				}
				continue
			}
			switch {
			case target == "/run/model-config/models.yaml":
				if e := in.addFile(rel, "models.yaml", "model-config"); e != nil {
					return nil, nil, fmt.Errorf("runtime input projection %d: %w", i, e)
				}
			case strings.HasPrefix(target, "/config/"):
				if e := in.addFile(rel, strings.TrimPrefix(target, "/config/"), "cfg"); e != nil {
					return nil, nil, fmt.Errorf("runtime input projection %d: %w", i, e)
				}
			case target == "/state/hermes/config.yaml" || target == "/state/hermes/SOUL.md":
				name := target[strings.LastIndex(target, "/")+1:]
				if e := in.addFile(rel, kubeHermesConfDir+"/"+name, "inputs"); e != nil {
					return nil, nil, fmt.Errorf("runtime input projection %d: %w", i, e)
				}
				pvcLinks = append(pvcLinks, "hermes-config:"+name)
			default:
				return nil, nil, fmt.Errorf("unsupported generated mount target %q", target)
			}
			continue
		}
		if target == "/state/hermes" {
			hermesRel = rel
		}
		if !kubeEnvFileTargets[target] {
			switch {
			case strings.HasPrefix(target, "/workspace/"):
				// A read-only subtree inside the writable workspace keeps kernel
				// enforcement through a dedicated read-only memory volume: the
				// init container snapshots the subtree so the runtime sees the
				// revision's content at the same resolved path.
				if !ro {
					continue
				}
				if _, e := fs.Stat(root.FS(), rel); e == nil {
					if size, e := kubeTreeSize(root.FS(), rel); e != nil || size > kubeRoViewBudget {
						return nil, nil, fmt.Errorf("workspace read-only subtree %q exceeds the staging budget", target)
					}
				} else if !errors.Is(e, fs.ErrNotExist) {
					return nil, nil, errors.New("runtime mount source unavailable")
				}
				roViews = append(roViews, rel)
			case strings.HasPrefix(target, kubeSharedRoot+"/"):
				sharedLinks = append(sharedLinks, strings.TrimPrefix(target, kubeSharedRoot+"/")+":"+persistPath(ro, rel))
			case kubeEnvDirTargets[target]:
				redirects = append(redirects, struct{ from, to string }{target, persistPath(ro, rel)})
			default:
				return nil, nil, fmt.Errorf("unsupported runtime mount target %q", target)
			}
			directories = append(directories, rel)
			continue
		}
		redirects = append(redirects, struct{ from, to string }{target, persistPath(ro, rel)})
	}
	if hermesRel == "" {
		return nil, nil, errors.New("runtime hermes home mount unavailable")
	}
	for _, name := range managedExtensionRootTmpfs {
		directories = append(directories, hermesRel+"/"+name)
	}
	// Apply env redirects longest-prefix first so e.g. /state/hermes wins over
	// /state and file targets like /state/ssh/config.yaml resolve under the
	// directory redirect of /state/ssh.
	sort.Slice(redirects, func(i, j int) bool { return len(redirects[i].from) > len(redirects[j].from) })
	for key, value := range values {
		for _, redirect := range redirects {
			if value == redirect.from || strings.HasPrefix(value, redirect.from+"/") {
				values[key] = redirect.to + strings.TrimPrefix(value, redirect.from)
				break
			}
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var environment []core.EnvVar
	for i, key := range keys {
		if !kubeEnvName.MatchString(key) || strings.ContainsRune(values[key], 0) {
			return nil, nil, errors.New("invalid runtime environment entry")
		}
		secretKey := fmt.Sprintf("e%04d", i)
		in.data[secretKey] = []byte(values[key])
		in.size += len(values[key])
		if in.size > kubeInputBudget || len(keys) > 512 {
			return nil, nil, errors.New("runtime environment limit exceeded")
		}
		environment = append(environment, core.EnvVar{Name: key, ValueFrom: &core.EnvVarSource{SecretKeyRef: &core.SecretKeySelector{LocalObjectReference: core.LocalObjectReference{Name: k.secretName(rt)}, Key: secretKey}}})
	}
	for _, field := range []struct{ name, path string }{{"HUB_KUBERNETES_POD_NAME", "metadata.name"}, {"HUB_KUBERNETES_POD_NAMESPACE", "metadata.namespace"}} {
		environment = append(environment, core.EnvVar{Name: field.name, ValueFrom: &core.EnvVarSource{FieldRef: &core.ObjectFieldSelector{FieldPath: field.path}}})
	}
	if len(in.groups) == 0 {
		return nil, nil, errors.New("runtime published input projection is empty")
	}
	// Secrets contain both files and env, but only the file allowlist is mounted.
	mode := int32(0440)
	immutable := true
	secret := &core.Secret{ObjectMeta: meta.ObjectMeta{Name: k.secretName(rt), Labels: k.labels(rt), Annotations: k.annotations(rt, true)}, Type: core.SecretTypeOpaque, Immutable: &immutable, Data: in.data}
	secretVolumes := map[string]string{"inputs": kubeInputsRoot, "cfg": "/config", "model-config": "/run/model-config"}
	mounts := []core.VolumeMount{{Name: "tmp", MountPath: "/tmp"}, {Name: "space", MountPath: kubePersistRoot}, {Name: "space", MountPath: kubePersistRoRoot, ReadOnly: true}, {Name: "private", MountPath: kubePrivateRoot}}
	for _, name := range []string{"inputs", "cfg", "model-config"} {
		if len(in.groups[name]) == 0 {
			continue
		}
		volumes = append(volumes, core.Volume{Name: name, VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: secret.Name, DefaultMode: &mode, Items: in.groups[name]}}})
		mounts = append(mounts, core.VolumeMount{Name: name, MountPath: secretVolumes[name], ReadOnly: true})
	}
	if len(sharedLinks) != 0 {
		volumes = append(volumes, core.Volume{Name: "shared", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{Medium: core.StorageMediumMemory}}})
		mounts = append(mounts, core.VolumeMount{Name: "shared", MountPath: kubeSharedRoot})
	}
	for i, rel := range roViews {
		name := fmt.Sprintf("roview-%d", i)
		volumes = append(volumes, core.Volume{Name: name, VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{Medium: core.StorageMediumMemory, SizeLimit: &kubeRoViewLimit}}})
		mounts = append(mounts, core.VolumeMount{Name: name, MountPath: kubePersistRoot + "/" + rel, ReadOnly: true})
	}
	uid, gid := int64(10001), int64(10001)
	no, yes := false, true
	security := &core.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &core.Capabilities{Drop: []core.Capability{"ALL"}}}
	initialArgs := []string{`umask 077; while [ "$#" -gt 0 ]; do kind="$1"; target="$2"; case "$kind" in d) mkdir -p "` + kubePersistRoot + `/$target"; shift 2;; w) mkdir -p "` + kubePrivateRoot + `/$target" && cp -RL "` + kubeInputsRoot + `/$target/." "` + kubePrivateRoot + `/$target/" && chmod -R u=rwX,go= "` + kubePrivateRoot + `/$target"; shift 2;; l) ln -sfn "$3" "` + kubePersistRoot + `/$target"; shift 3;; s) mkdir -p "$(dirname "` + kubeSharedRoot + `/$target")" && ln -sfn "$3" "` + kubeSharedRoot + `/$target"; shift 3;; c) if [ -d "` + kubePersistRoot + `/$3" ]; then cp -RL "` + kubePersistRoot + `/$3/." "/$target/"; fi; find "/$target" -mindepth 1 -exec chmod -R a-w {} +; shift 3;; *) exit 1;; esac; done`, "prepare"}
	slices.Sort(directories)
	directories = slices.Compact(directories)
	for _, dir := range directories {
		initialArgs = append(initialArgs, "d", dir)
	}
	for _, proj := range privateCopies {
		initialArgs = append(initialArgs, "w", proj)
	}
	for _, link := range pvcLinks {
		name := link[strings.Index(link, ":")+1:]
		initialArgs = append(initialArgs, "l", hermesRel+"/"+name, kubeInputsRoot+"/"+kubeHermesConfDir+"/"+name)
	}
	for _, link := range sharedLinks {
		name := link[:strings.Index(link, ":")]
		initialArgs = append(initialArgs, "s", name, link[strings.Index(link, ":")+1:])
	}
	for i, rel := range roViews {
		initialArgs = append(initialArgs, "c", fmt.Sprintf("roview-%d", i), rel)
	}
	image, _, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, settings.Environment, k.m.cfg.Image)
	if err != nil {
		return nil, nil, errors.New("runtime image selection unavailable")
	}
	cpu, err := resource.ParseQuantity(k.m.cfg.CPU)
	if err != nil || cpu.Sign() <= 0 {
		return nil, nil, errors.New("invalid runtime CPU limit")
	}
	memoryText := k.m.cfg.Memory
	if last := strings.ToUpper(memoryText[len(memoryText)-1:]); last == "G" || last == "M" || last == "K" {
		memoryText = memoryText[:len(memoryText)-1] + last + "i"
	}
	memory, err := resource.ParseQuantity(memoryText)
	if err != nil || memory.Sign() <= 0 {
		return nil, nil, errors.New("invalid runtime memory limit")
	}
	requestCPU, requestMemory := resource.MustParse("100m"), resource.MustParse("128Mi")
	if cpu.Cmp(requestCPU) < 0 {
		requestCPU = cpu
	}
	if memory.Cmp(requestMemory) < 0 {
		requestMemory = memory
	}
	probe := &core.Probe{ProbeHandler: core.ProbeHandler{TCPSocket: &core.TCPSocketAction{Port: intstr.FromInt(k.m.cfg.RuntimePort)}}, PeriodSeconds: 2, TimeoutSeconds: 1, FailureThreshold: 3}
	initMounts := []core.VolumeMount{{Name: "space", MountPath: kubePersistRoot}, {Name: "private", MountPath: kubePrivateRoot}}
	if len(in.groups["inputs"]) != 0 {
		initMounts = append(initMounts, core.VolumeMount{Name: "inputs", MountPath: kubeInputsRoot, ReadOnly: true})
	}
	if len(sharedLinks) != 0 {
		initMounts = append(initMounts, core.VolumeMount{Name: "shared", MountPath: kubeSharedRoot})
	}
	for i := range roViews {
		initMounts = append(initMounts, core.VolumeMount{Name: fmt.Sprintf("roview-%d", i), MountPath: "/roview-" + strconv.Itoa(i)})
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: rt.Container, Finalizers: []string{kubeComputeFence}, Labels: k.labels(rt), Annotations: k.annotations(rt, true)}, Spec: core.PodSpec{AutomountServiceAccountToken: &no, EnableServiceLinks: &no, RestartPolicy: core.RestartPolicyNever, SecurityContext: &core.PodSecurityContext{RunAsUser: &uid, RunAsGroup: &gid, RunAsNonRoot: &yes, FSGroup: &gid, SeccompProfile: &core.SeccompProfile{Type: core.SeccompProfileTypeRuntimeDefault}}, Volumes: volumes, InitContainers: []core.Container{{Name: "private-inputs", Image: image, Command: []string{"/bin/sh", "-ec"}, Args: initialArgs, SecurityContext: security, VolumeMounts: initMounts, Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("50m"), core.ResourceMemory: resource.MustParse("32Mi")}, Limits: core.ResourceList{core.ResourceCPU: resource.MustParse("500m"), core.ResourceMemory: resource.MustParse("128Mi")}}}}, Containers: []core.Container{{Name: "runtime", Image: image, Args: []string{"serve"}, Env: environment, VolumeMounts: mounts, SecurityContext: security, ReadinessProbe: probe, Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: requestCPU, core.ResourceMemory: requestMemory}, Limits: core.ResourceList{core.ResourceCPU: cpu, core.ResourceMemory: memory}}}}}}
	if k.cfg.RuntimeClassName != "" {
		pod.Spec.RuntimeClassName = &k.cfg.RuntimeClassName
	}
	for _, name := range k.cfg.ImagePullSecrets {
		pod.Spec.ImagePullSecrets = append(pod.Spec.ImagePullSecrets, core.LocalObjectReference{Name: name})
	}
	return pod, secret, nil
}
