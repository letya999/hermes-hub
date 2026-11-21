package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktest "k8s.io/client-go/testing"
)

func TestSandboxRelayDiscovery(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "ambiguous", "loopback", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "ps" {
					return []byte("aaa\nbbb\n"), nil
				}
				if scenario == "unreadable" {
					return nil, errors.New("unavailable")
				}
				ip, aliases := "172.19.0.2", []string{"model-relay"}
				if args[len(args)-1] == "bbb" {
					ip, aliases = "172.19.0.3", []string{"toolhub", "other-control"}
				}
				if scenario == "missing" {
					aliases = nil
				}
				if scenario == "ambiguous" {
					aliases = []string{"toolhub"}
				}
				if scenario == "loopback" {
					ip = "127.0.0.1"
				}
				return json.Marshal(map[string]any{"IPAddress": ip, "Aliases": aliases})
			}, nil)
			args, err := m.sandboxRelayHosts(t.Context(), stack.Settings{User: "alice"}, t.TempDir())
			if scenario != "valid" {
				if err == nil {
					t.Fatal("unsafe discovery accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(args, " ") != "--add-host toolhub:172.19.0.3 --add-host model-relay:172.19.0.2" {
				t.Fatalf("relay pins=%v", args)
			}
		})
	}
}

func TestSandboxSelectionPersistsAndRefusesWarmChange(t *testing.T) {
	m, _ := testManager(t, nil, nil)
	m.cfg.ContainerRuntime = "runsc"
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(m.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedManager
	if err = json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.ContainerRuntime != "runsc" {
		t.Fatal("sandbox selection not persisted")
	}
	state.Items = []persistedRuntime{{Runtime: Runtime{State: Ready}}}
	body, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(m.statePath, body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.ContainerRuntime = "runc"
	if _, err = newTestManager(t, cfg); err == nil || !strings.Contains(err.Error(), "requires stopped runtimes") {
		t.Fatalf("warm downgrade=%v", err)
	}
}

func TestSandboxKubernetesClassCannotDisappear(t *testing.T) {
	m, root, client := kubeManager(t)
	writeManagedRevisionFixtures(t, root)
	m.cfg.ContainerRuntime = "runsc"
	m.cfg.Kubernetes.RuntimeClassName = ""
	k, err := newKubernetesBackend(m)
	if err != nil {
		t.Fatal(err)
	}
	m.backend = k
	if k.cfg.RuntimeClassName != "runsc" {
		t.Fatal("sandbox became cluster default")
	}
	m.cfg.Kubernetes.RuntimeClassName = "runc"
	if _, err := newKubernetesBackend(m); err == nil {
		t.Fatal("conflicting runc class accepted for sandbox")
	}
	m.cfg.Kubernetes = k.cfg
	var attempted int
	client.PrependReactor("create", "pods", func(action ktest.Action) (bool, kruntime.Object, error) {
		pod := action.(ktest.CreateAction).GetObject().(*core.Pod)
		if pod.Annotations[kubeRole] == "relay" {
			return false, nil, nil
		}
		attempted++
		if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "runsc" {
			t.Fatal("missing class on selected sandbox")
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, pod.Name, errors.New("RuntimeClass absent"))
	})
	if _, err = m.Ensure(t.Context(), binding(root)); err == nil || !strings.Contains(err.Error(), "RuntimeClass") {
		t.Fatalf("missing class did not fail: %v", err)
	}
	if attempted != 1 {
		t.Fatalf("fallback/retry attempts=%d", attempted)
	}
	// Class mismatch must also refuse adoption/observation of already-owned Pods.
	rt := Runtime{Container: "owned", PrincipalID: "alice", ContextID: "alice", RuntimeMode: "gateway", Generation: "gen-1"}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: rt.Container, Namespace: k.cfg.Namespace, Annotations: k.annotations(rt, true)}}
	if err := client.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	if err := k.Verify(t.Context(), rt); err == nil || !strings.Contains(err.Error(), "isolation mismatch") {
		t.Fatalf("weaker Pod adoption=%v", err)
	}
	if _, err := k.Observe(t.Context(), rt); err == nil || !strings.Contains(err.Error(), "isolation mismatch") {
		t.Fatalf("weaker Pod observation=%v", err)
	}
}
