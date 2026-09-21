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
		"-----BEGIN PRIVATE KEY-----",
		"AKIAIOSFODNN7EXAMPLE", "github_pat_11AAA22bbb33CCC44ddE5",
		"ghp_a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8",
		"xoxb-1234567890ab-CDEF12345678-zyxwvutsrq",
		"sk-proj-Ab1Cd2Ef3Gh4Ij5Kl6Mn7Op8Qr9St0Uv",
		"AIzaSyD4iE5fG6hI7jK8lM9nO0pQ1rS2tU3vW4x",
	} {
		if err := VerifyArtifactContextNoSecrets(makeContext(secret)); err == nil {
			t.Fatalf("secret-like content accepted: %q", secret)
		}
	}
	for _, fixture := range []string{
		"github_pat_" + strings.Repeat("x", 23), "gho_" + strings.Repeat("x", 36),
		"ghp_" + strings.Repeat("x", 36), "ghu_" + strings.Repeat("x", 36),
		"ghs_" + strings.Repeat("x", 36), "ghr_" + strings.Repeat("0", 36),
		"xoxb-" + strings.Repeat("x", 12) + "-" + strings.Repeat("x", 12),
		"AKIA" + strings.Repeat("0", 16), "sk-" + strings.Repeat("x", 32),
		"AIza" + strings.Repeat("x", 35),
	} {
		if err := VerifyArtifactContextNoSecrets(makeContext(fixture)); err != nil {
			t.Fatalf("uniform placeholder fixture rejected: %v", err)
		}
	}
	if err := VerifyArtifactContextNoSecrets([]byte("not tar")); err == nil {
		t.Fatal("malformed context accepted")
	}
}
