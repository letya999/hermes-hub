package api

import (
	"archive/tar"
	"bytes"
	"slices"
	"testing"
)

func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDiscoverOpenAPIEgressSources(t *testing.T) {
	ctx := tarBytes(t, map[string]string{
		"openapi.json":        `{"servers":[{"url":"https://api.slack.com/methods"},{"url":"http://insecure.example.com"},{"url":"https://u:p@x.com"}]}`,
		"client.go":           `const base = "https://api.atlassian.com/rest"`,
		"vendor/dep.go":       `const v = "https://vendored.example.io"`,
		"testdata/fixture.go": `const f = "https://fixture-host.io"`,
		"server_test.go":      `const t = "https://test-host.io"`,
		"notes.txt":           `https://not-scanned.io`,
		"dir/deep.py":         `url = "https://api.github.com/v3"`,
	})
	hosts := DiscoverOpenAPIEgress(ctx)
	if !slices.Contains(hosts, "api.slack.com") || !slices.Contains(hosts, "api.atlassian.com") || !slices.Contains(hosts, "api.github.com") {
		t.Fatalf("egress hosts: %v", hosts)
	}
	// vendored code is scanned for egress (only test fixtures are excluded)
	if !slices.Contains(hosts, "vendored.example.io") {
		t.Fatalf("vendored literal skipped: %v", hosts)
	}
	for _, bad := range []string{"insecure.example.com", "fixture-host.io", "test-host.io", "not-scanned.io"} {
		if slices.Contains(hosts, bad) {
			t.Fatalf("%q leaked into egress", bad)
		}
	}
	if got := DiscoverOpenAPIEgress([]byte("not a tar")); len(got) != 0 {
		t.Fatalf("garbage tar: %v", got)
	}
}
