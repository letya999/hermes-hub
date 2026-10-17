package api

import (
	"bytes"
	"context"
	"io"
	"slices"
	"testing"
)

// The pinned builder ships busybox nc: only -w exists, -W exits with usage.
// A probe exec that passes -W never reaches the proxy and wedges every build.
func TestRestrictedBuildEgressProbeUsesBusyboxNc(t *testing.T) {
	var probe []string
	run := func(_ context.Context, input io.Reader, args ...string) ([]byte, error) {
		probe = append([]string(nil), args...)
		request, _ := io.ReadAll(input)
		if bytes.Contains(request, []byte("example.com")) {
			return []byte("HTTP/1.1 403 Forbidden\r\n"), nil
		}
		return []byte("HTTP/1.1 200 Connection established\r\n"), nil
	}
	if err := verifyRestrictedBuildEgress(context.Background(), run, "builder", "proxy"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(probe, "-W") || !slices.Contains(probe, "-w") {
		t.Fatalf("egress probe nc timeout flag must be busybox -w: %v", probe)
	}
}
