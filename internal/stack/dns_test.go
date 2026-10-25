package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doctor validates the operator DNS profile when present: an unloadable or
// fail-closed profile surfaces as an issue; absent config stays silent.
func TestDoctorDNSProfile(t *testing.T) {
	d := t.TempDir()
	s := Settings{SpaceDir: d, Model: "m", ModelURL: "http://x/v1"}
	cfgDir := filepath.Join(d, "connections", "dns")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bad := "schema: 1\nprovider: regru\nzone: example.com\ncredential_ref: \"file:creds.json\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if !hasIssue(Doctor(s, map[string]string{"OPENAI_API_KEY": "k"}), "dns") {
		t.Fatal("doctor must flag a fail-closed dns profile")
	}
	good := "schema: 1\nprovider: digitalocean\nzone: hub.example.com\ncredential_ref: \"file:creds.json\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, issue := range Doctor(s, map[string]string{"OPENAI_API_KEY": "k"}) {
		if strings.Contains(issue, "dns") {
			t.Fatalf("doctor still flags dns after fix: %s", issue)
		}
	}
}

func hasIssue(issues []string, sub string) bool {
	for _, i := range issues {
		if strings.Contains(i, sub) {
			return true
		}
	}
	return false
}
