//go:build integration

package toolhub

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPreparedProbeProxyUsesInternalNetwork(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker unavailable")
	}
	network, proxyURL, stop, err := startPreparedProbeProxy(t.Context(), []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !strings.HasPrefix(proxyURL, "http://") || !strings.HasSuffix(proxyURL, ":3128") {
		t.Fatalf("invalid managed proxy URL: %q", proxyURL)
	}
	output, err := exec.Command("docker", "network", "inspect", "--format", "{{.Internal}}", network).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "true" {
		t.Fatalf("credentialed probe network is not internal: %s %v", output, err)
	}
}
