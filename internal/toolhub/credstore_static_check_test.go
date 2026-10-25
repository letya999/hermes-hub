package toolhub

// #287 static check: the Credential Broker is the only credential authority.
// Direct local-credstore access is frozen to the migration seam below — the
// wiring, the uncontracted-legacy exception (credential gate / remote-MCP
// OAuth), lifecycle finalizers and the migrator itself. Any new file that
// starts reading the local store fails this test, so a connector call path
// cannot silently re-acquire a direct backend.
import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// credstoreSeam is the exhaustive allowlist of non-test files that may
// reference the legacy store. Shrinking it is welcome; growing it requires
// editing this test, which is exactly the friction the gate exists to add.
var credstoreSeam = map[string]bool{
	"broker.go":              true, // dual-read exception check
	"control.go":             true, // Secrets field declaration
	"control_binding.go":     true, // form-credential rotation writes backend refs
	"control_credentials.go": true, // credential submit/status surface
	"credential_migrate.go":  true, // the migrator
	"prepared_oauth.go":      true, // OAuth callback token hop into the leased state dir
	"reconcile_seams.go":     true, // drift reconciler
	"remote_oauth.go":        true, // remote-MCP OAuth token set (uncontracted)
	"remove_finalizers.go":   true, // teardown deletes
	"server.go":              true, // backend wiring
}

var credstoreAPISeam = map[string]bool{
	"inject.go": true, // the legacy injector invoked through the merge seam
}

var (
	credstoreImport   = regexp.MustCompile(`internal/credstore"`)
	secretsMethodCall = regexp.MustCompile(`\.Secrets\.(Get|Put|Delete|SetStatus|Info|List)\(`)
)

func TestNoDirectCredstoreOutsideMigrationSeam(t *testing.T) {
	pkgs := map[string]map[string]bool{
		".":   credstoreSeam,
		"api": credstoreAPISeam,
	}
	var offenders []string
	for dir, seam := range pkgs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			hits := append(credstoreImport.FindAllString(string(data), -1), secretsMethodCall.FindAllString(string(data), -1)...)
			if len(hits) > 0 && !seam[name] {
				offenders = append(offenders, filepath.Join("internal/toolhub", dir, name))
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("direct local credstore access outside the migration seam — "+
			"route connector credentials through the Broker: %s", strings.Join(offenders, ", "))
	}
}
