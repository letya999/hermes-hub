package devcheck

import (
	"bytes"
	"context"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func helmRender(t *testing.T, release string, flags ...string) []map[string]any {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm CLI required; just helm-check enforces the prerequisite")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	args := append([]string{"template", release, "../../deploy/helm/hermes-hub", "--namespace", "contract"}, flags...)
	out, err := exec.CommandContext(ctx, "helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v: %s", err, out)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(out))
	var objects []map[string]any
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			return objects
		} else if err != nil {
			t.Fatalf("rendered YAML: %v", err)
		}
		if object != nil {
			objects = append(objects, object)
		}
	}
}

func helmMap(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("expected object before %q, got %T", key, value)
		}
		value = object[key]
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object at %v, got %T", path, value)
	}
	return object
}

func helmList(t *testing.T, value any) []any {
	t.Helper()
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("expected list, got %T", value)
	}
	return list
}

func TestHelmChartKubernetesBackend(t *testing.T) {
	counts := map[string]int{}
	for _, object := range helmRender(t, "alpha", "--set", "execution.backend=kubernetes", "--set", "execution.kubernetes.runtimeClassName=sandbox", "--set", "execution.kubernetes.apiServerCIDRs[0]=10.96.0.1/32") {
		kind := object["kind"].(string)
		counts[kind]++
		if kind == "Role" {
			if helmMap(t, object, "metadata")["namespace"] != "contract-runtimes" {
				t.Fatal("runtime authority escaped environment namespace")
			}
			for _, raw := range helmList(t, object["rules"]) {
				rule := helmMap(t, raw)
				for _, resource := range helmList(t, rule["resources"]) {
					for _, verb := range helmList(t, rule["verbs"]) {
						if resource == "persistentvolumeclaims" && verb == "delete" || resource == "*" || verb == "*" {
							t.Fatal("unbounded runtime authority")
						}
					}
				}
			}
		}
		if kind != "Deployment" {
			continue
		}
		template := helmMap(t, object, "spec", "template")
		component := helmMap(t, template, "metadata", "labels")["app.kubernetes.io/component"]
		pod := helmMap(t, template, "spec")
		if pod["automountServiceAccountToken"] != false {
			t.Fatal("implicit API token enabled")
		}
		found := false
		for _, volume := range helmList(t, pod["volumes"]) {
			if helmMap(t, volume)["name"] == "api-identity" {
				found = true
			}
		}
		if found != (component == "supervisor") {
			t.Fatal("API authority projected outside supervisor")
		}
		if component == "supervisor" {
			container := helmMap(t, helmList(t, pod["containers"])[0])
			values := map[string]any{}
			for _, raw := range helmList(t, container["env"]) {
				variable := helmMap(t, raw)
				values[variable["name"].(string)] = variable["value"]
			}
			if values["HUB_EXECUTION_BACKEND"] != "kubernetes" || values["HUB_RUNTIME_CLASS_NAME"] != "sandbox" || values["HUB_KUBERNETES_NAMESPACE"] != "contract-runtimes" {
				t.Fatal("runtime backend configuration missing")
			}
		}
	}
	if counts["Role"] != 1 || counts["RoleBinding"] != 1 || counts["ServiceAccount"] != 1 || counts["ClusterRole"] != 0 {
		t.Fatal("runtime RBAC scope changed")
	}
}

func TestHelmChartContract(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("Helm CLI required; run just helm-check")
	}
	t.Run("default isolation and single writers", func(t *testing.T) {
		counts := map[string]int{}
		for _, object := range helmRender(t, "alpha") {
			kind := object["kind"].(string)
			counts[kind]++
			meta := helmMap(t, object, "metadata")
			if !strings.HasPrefix(meta["name"].(string), "alpha-hermes-hub-") {
				t.Fatalf("resource is not release scoped: %s", meta["name"])
			}
			if kind == "PersistentVolumeClaim" {
				if helmMap(t, meta, "annotations")["helm.sh/resource-policy"] != "keep" {
					t.Fatal("uninstall would delete a state claim")
				}
				continue
			}
			if kind == "Service" {
				if helmMap(t, object, "spec")["type"] != "ClusterIP" {
					t.Fatal("internal service exposed outside the cluster")
				}
				continue
			}
			if kind == "NetworkPolicy" {
				spec := helmMap(t, object, "spec")
				component := helmMap(t, spec, "podSelector", "matchLabels")["app.kubernetes.io/component"]
				if helmMap(t, spec, "podSelector", "matchLabels")["app.kubernetes.io/instance"] != "alpha" {
					t.Fatal("network policy selects another release")
				}
				world := 0
				for _, raw := range helmList(t, spec["egress"]) {
					rule := helmMap(t, raw)
					for _, raw := range helmList(t, rule["to"]) {
						peer := helmMap(t, raw)
						if peer["ipBlock"] != nil {
							world++
							if component != "communication-hub" && component != "cliproxy" {
								t.Fatal("internal control service gained public egress")
							}
							port := helmMap(t, helmList(t, rule["ports"])[0])
							if port["port"] != 443 || len(helmList(t, helmMap(t, peer, "ipBlock")["except"])) == 0 {
								t.Fatal("public egress must be HTTPS with private ranges excluded")
							}
							block := helmMap(t, peer, "ipBlock")
							parent, err := netip.ParsePrefix(block["cidr"].(string))
							if err != nil {
								t.Fatal(err)
							}
							for _, raw := range helmList(t, block["except"]) {
								child, err := netip.ParsePrefix(raw.(string))
								if err != nil || child.Addr().Is4In6() || child.Bits() <= parent.Bits() || !parent.Contains(child.Addr()) {
									t.Fatalf("invalid strict-subset policy exception %s in %s", raw, parent)
								}
							}
						}
					}
				}
				if (component == "communication-hub" || component == "cliproxy") && world != 2 {
					t.Fatal("channel/model public egress is missing an address family")
				}
				continue
			}
			if kind != "Deployment" {
				continue
			}
			spec := helmMap(t, object, "spec")
			if spec["replicas"] != 1 || helmMap(t, spec, "strategy")["type"] != "Recreate" {
				t.Fatal("file-backed services must have exactly one writer during rollouts")
			}
			pod := helmMap(t, spec, "template", "spec")
			if _, exists := pod["runtimeClassName"]; exists {
				t.Fatal("empty runtimeClassName must select the cluster default")
			}
			if pod["automountServiceAccountToken"] != false {
				t.Fatal("control plane unexpectedly has Kubernetes API authority")
			}
			security := helmMap(t, pod, "securityContext")
			if security["runAsNonRoot"] != true || security["runAsUser"] != 10001 || helmMap(t, security, "seccompProfile")["type"] != "RuntimeDefault" {
				t.Fatal("Pod lost non-root/seccomp isolation")
			}
			for _, raw := range helmList(t, pod["volumes"]) {
				volume := helmMap(t, raw)
				if volume["hostPath"] != nil {
					t.Fatal("hostPath violates the portable deployment boundary")
				}
			}
			containers := helmList(t, pod["containers"])
			if init, ok := pod["initContainers"].([]any); ok {
				containers = append(containers, init...)
			}
			for _, raw := range containers {
				container := helmMap(t, raw)
				if container["name"] == "supervisor" {
					command := helmList(t, container["command"])
					if len(command) != 2 || command[0] != "hubctl" || command[1] != "supervisor" {
						t.Fatal("supervisor must use the entrypoint shipped in the control image")
					}
				}
				security := helmMap(t, container, "securityContext")
				if security["privileged"] == true || security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != true {
					t.Fatal("container can escalate or write its root filesystem")
				}
				if helmList(t, helmMap(t, security, "capabilities")["drop"])[0] != "ALL" {
					t.Fatal("container retains capabilities")
				}
				seen := map[string]bool{}
				if env, ok := container["env"].([]any); ok {
					for _, raw := range env {
						variable := helmMap(t, raw)
						name := variable["name"].(string)
						if seen[name] {
							t.Fatalf("duplicate environment variable %s", name)
						}
						seen[name] = true
						if strings.HasSuffix(name, "_AUTH") || strings.HasSuffix(name, "_TOKEN") {
							helmMap(t, variable, "valueFrom", "secretKeyRef")
						}
					}
				}
			}
		}
		for _, kind := range []string{"Deployment", "Service", "PersistentVolumeClaim", "NetworkPolicy"} {
			if counts[kind] != 5 {
				t.Fatalf("%s count = %d, want 5", kind, counts[kind])
			}
		}
		if counts["Secret"]+counts["Ingress"]+counts["ExternalSecret"]+counts["ServiceMonitor"] != 0 {
			t.Fatal("optional integrations or secret payloads rendered by default")
		}
	})
	t.Run("values and authenticated metrics", func(t *testing.T) {
		values := filepath.Join(t.TempDir(), "external.yaml")
		if err := os.WriteFile(values, []byte("externalSecrets:\n  - name: referenced-control\n    secretStoreRef: {name: reviewed-store, kind: SecretStore}\n    data:\n      - secretKey: supervisor-token\n        remoteRef: {key: hub/supervisor}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		objects := helmRender(t, "beta", "-f", values, "--set", "runtimeClassName=sandbox", "--set", "ingress.enabled=true", "--set", "serviceMonitor.enabled=true",
			"--set", "persistence.spaces.existingClaim=existing-spaces", "--set-string", "persistence.spool.storageClass=",
			"--set", "services.toolhub.image.repository=registry.example.invalid/hub", "--set", "services.toolhub.image.digest=sha256:"+strings.Repeat("a", 64))
		counts := map[string]int{}
		reused := 0
		for _, object := range objects {
			kind := object["kind"].(string)
			counts[kind]++
			spec := helmMap(t, object, "spec")
			if kind == "Deployment" {
				pod := helmMap(t, spec, "template", "spec")
				if pod["runtimeClassName"] != "sandbox" {
					t.Fatal("runtimeClassName did not reach every Pod")
				}
				for _, raw := range helmList(t, pod["volumes"]) {
					volume := helmMap(t, raw)
					if volume["name"] == "spaces" {
						if helmMap(t, volume, "persistentVolumeClaim")["claimName"] != "existing-spaces" {
							t.Fatal("existing spaces claim is not mounted")
						}
						reused++
					}
				}
				for _, raw := range helmList(t, pod["containers"]) {
					container := helmMap(t, raw)
					if container["name"] == "toolhub" && container["image"] != "registry.example.invalid/hub@sha256:"+strings.Repeat("a", 64) {
						t.Fatal("image digest did not take precedence over tag")
					}
				}
			} else if kind == "ServiceMonitor" {
				component := helmMap(t, object, "metadata", "labels")["app.kubernetes.io/component"].(string)
				if component != "supervisor" && component != "communication-hub" {
					t.Fatal("scraping a service without the implemented metrics contract")
				}
				endpoint := helmMap(t, helmList(t, spec["endpoints"])[0])
				if endpoint["path"] != "/v1/metrics" || endpoint["port"] != "http" {
					t.Fatal("incorrect metrics endpoint")
				}
				auth := helmMap(t, endpoint, "authorization")
				if auth["type"] != "Bearer" || helmMap(t, auth, "credentials")["name"] != "hermes-hub-control" {
					t.Fatal("metrics lost its Secret-backed authorization")
				}
			} else if kind == "ExternalSecret" {
				if object["apiVersion"] != "external-secrets.io/v1" || helmMap(t, spec, "secretStoreRef")["name"] != "reviewed-store" || helmMap(t, spec, "target")["name"] != "referenced-control" {
					t.Fatal("external secret lost its explicit store or target")
				}
				ref := helmMap(t, helmList(t, spec["data"])[0])
				if ref["secretKey"] != "supervisor-token" || helmMap(t, ref, "remoteRef")["key"] != "hub/supervisor" {
					t.Fatal("external secret mapping changed")
				}
			} else if kind == "PersistentVolumeClaim" && helmMap(t, object, "metadata")["name"] == "beta-hermes-hub-spaces" {
				t.Fatal("an existing claim must not be recreated")
			}
		}
		if counts["ServiceMonitor"] != 2 || counts["Ingress"] != 1 || counts["PersistentVolumeClaim"] != 4 || counts["ExternalSecret"] != 1 || reused != 3 {
			t.Fatalf("optional resource counts: %v", counts)
		}
	})
	t.Run("invalid values fail at the boundary", func(t *testing.T) {
		for _, override := range []string{"environment=test", "services.toolhub.port=80", "services.toolhub.image.digest=bad", "ingress.gatewayPaths[0]=/", "services.toolhub.env.HOME=/root"} {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			out, err := exec.CommandContext(ctx, "helm", "template", "invalid", "../../deploy/helm/hermes-hub", "--set", override).CombinedOutput()
			cancel()
			if err == nil || !strings.Contains(string(out), "values don't meet") {
				t.Fatalf("%s: expected schema rejection, got %v: %s", override, err, out)
			}
		}
	})
}
