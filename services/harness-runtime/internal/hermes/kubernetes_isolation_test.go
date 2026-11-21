package hermes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesManagedIsolationRequiresBoundReadOnlyReceipt(t *testing.T) {
	home := managedIsolationFixture(t)
	t.Setenv("HUB_EXECUTION_SUBSTRATE", "kubernetes")
	for key, value := range map[string]string{"HUB_PRINCIPAL_ID": "alice", "HUB_CONTEXT_ID": "alice", "HUB_RUNTIME_MODE": "gateway", "HUB_RUNTIME_GENERATION": "gen-1", "HUB_KUBERNETES_POD_NAMESPACE": "runtime-prod", "HUB_KUBERNETES_POD_NAME": "runtime-a"} {
		t.Setenv(key, value)
	}
	oldPath, oldIPs := kubernetesIsolationPath, managedLookupIPs
	t.Cleanup(func() { kubernetesIsolationPath, managedLookupIPs = oldPath, oldIPs })
	kubernetesIsolationPath = filepath.Join(t.TempDir(), "policy.json")
	managedLookupIPs = func(string) ([]string, error) { return []string{"10.1.0.2"}, nil }
	managedReadRoutes = func() ([]byte, []byte, error) {
		return []byte("Iface Destination Gateway Flags\neth0 00000000 0100000A 0003\n"), nil, nil
	}
	if err := verifyManagedIsolation(home); err == nil {
		t.Fatal("environment flag alone bypassed isolation")
	}
	hash := sha256.Sum256([]byte("alice\x00alice\x00gateway"))
	receipt := KubernetesIsolationReceipt{Namespace: "runtime-prod", Pod: "runtime-a", Generation: "gen-1", ContextHash: hex.EncodeToString(hash[:]), PolicyUID: "policy-uid", PolicySHA256: strings.Repeat("a", 64), RelayIP: "10.1.0.2"}
	write := func(value KubernetesIsolationReceipt) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(kubernetesIsolationPath, 0600)
		if err := os.WriteFile(kubernetesIsolationPath, data, 0400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(kubernetesIsolationPath, 0400); err != nil {
			t.Fatal(err)
		}
	}
	write(receipt)
	if err := verifyManagedIsolation(home); err != nil {
		t.Fatalf("valid Kubernetes receipt rejected: %v", err)
	}
	foreign := receipt
	foreign.Pod = "another-pod"
	write(foreign)
	if err := verifyManagedIsolation(home); err == nil {
		t.Fatal("foreign Pod receipt accepted")
	}
	write(receipt)
	managedLookupIPs = func(string) ([]string, error) { return []string{"10.1.0.3"}, nil }
	if err := verifyManagedIsolation(home); err == nil {
		t.Fatal("private relay substitution accepted")
	}
	managedLookupIPs = func(string) ([]string, error) { return []string{"10.1.0.2"}, nil }
	if err := os.Chmod(kubernetesIsolationPath, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyManagedIsolation(home); err == nil {
		t.Fatal("writable receipt accepted")
	}
	write(receipt)
	t.Setenv("HUB_EXECUTION_SUBSTRATE", "docker")
	if err := verifyManagedIsolation(home); err == nil {
		t.Fatal("Kubernetes receipt relaxed Docker default-route check")
	}
}
