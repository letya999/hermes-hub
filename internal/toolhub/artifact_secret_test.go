package toolhub

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

func TestArtifactContextSecretGate(t *testing.T) {
	makeContext := func(value string) []byte {
		var output bytes.Buffer
		w := tar.NewWriter(&output)
		_ = w.WriteHeader(&tar.Header{Name: "source.txt", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(value))})
		_, _ = w.Write([]byte(value))
		_ = w.Close()
		return output.Bytes()
	}
	if err := VerifyArtifactContextNoSecrets(makeContext("ordinary MCP source")); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"-----BEGIN PRIVATE KEY-----", "AKIA" + strings.Repeat("A", 16),
		"github_pat_" + strings.Repeat("a", 24), "ghp_" + strings.Repeat("a", 36),
		"xoxb-" + strings.Repeat("a", 24), "sk-" + strings.Repeat("a", 32), "AIza" + strings.Repeat("a", 35),
	} {
		if err := VerifyArtifactContextNoSecrets(makeContext(secret)); err == nil {
			t.Fatal("secret-like content accepted")
		}
	}
	if err := VerifyArtifactContextNoSecrets([]byte("not tar")); err == nil {
		t.Fatal("malformed context accepted")
	}
}
