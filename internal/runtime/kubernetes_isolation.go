package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const KubernetesIsolationPath = "/run/hub-isolation/policy.json"

// KubernetesIsolationReceipt is a controller-authored private input, never an
// assertion accepted from the agent or from an environment flag alone.
type KubernetesIsolationReceipt struct {
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Generation   string `json:"generation"`
	ContextHash  string `json:"context_hash"`
	PolicyUID    string `json:"policy_uid"`
	PolicySHA256 string `json:"policy_sha256"`
	RelayIP      string `json:"relay_ip"`
}

var kubernetesIsolationPath = KubernetesIsolationPath

func verifyKubernetesIsolationReceipt() error {
	file, err := os.Open(kubernetesIsolationPath)
	if err != nil {
		return errors.New("Kubernetes isolation receipt unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return errors.New("Kubernetes isolation receipt invalid")
	}
	if writable, err := os.OpenFile(kubernetesIsolationPath, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = writable.Close()
		return errors.New("Kubernetes isolation receipt is writable")
	}
	var receipt KubernetesIsolationReceipt
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("Kubernetes isolation receipt malformed")
	}
	hash := sha256.Sum256([]byte(os.Getenv("HUB_PRINCIPAL_ID") + "\x00" + os.Getenv("HUB_CONTEXT_ID") + "\x00" + os.Getenv("HUB_RUNTIME_MODE")))
	digest, decodeErr := hex.DecodeString(receipt.PolicySHA256)
	if receipt.Namespace == "" || receipt.Namespace != os.Getenv("HUB_KUBERNETES_POD_NAMESPACE") || receipt.Pod == "" || receipt.Pod != os.Getenv("HUB_KUBERNETES_POD_NAME") || receipt.Generation == "" || receipt.Generation != os.Getenv("HUB_RUNTIME_GENERATION") || receipt.ContextHash != hex.EncodeToString(hash[:]) || receipt.PolicyUID == "" || len(receipt.PolicyUID) > 128 || decodeErr != nil || len(digest) != 32 || receipt.RelayIP == "" {
		return errors.New("Kubernetes isolation receipt binding mismatch")
	}
	for _, host := range []string{"toolhub", "model-relay"} {
		addresses, err := managedLookupIPs(host)
		if err != nil || len(addresses) == 0 {
			return errors.New("Kubernetes private relay address unavailable")
		}
		for _, address := range addresses {
			if address != receipt.RelayIP {
				return errors.New("Kubernetes private relay address mismatch")
			}
		}
	}
	return nil
}
