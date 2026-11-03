package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

func TestZZReproSpawnArgs(t *testing.T) {
	root := `C:\Users\User\a_projects\hermes-hub\spaces\artem_two`
	settings, err := stack.ReadEnvironment(root, "dev")
	if err != nil {
		t.Fatalf("ReadEnvironment: %v", err)
	}
	t.Logf("capability_mode=%q passport_ws=%v", settings.CapabilityMode, settings.PassportWorkspace != nil)
	if settings.PassportWorkspace != nil {
		plan := stack.MountPlan(root, "dev", root+`\managed\dev`, settings, *settings.PassportWorkspace)
		for _, pm := range plan {
			t.Logf("mount src=%s dst=%s ro=%v", pm.Source, pm.Target, pm.ReadOnly)
		}
	}
	if _, err := stack.MaterializeEffectiveSkills(root, settings); err != nil {
		t.Fatalf("MaterializeEffectiveSkills: %v", err)
	}
	if _, err := stack.MaterializeToolPolicySnapshot(root, settings); err != nil {
		t.Fatalf("MaterializeToolPolicySnapshot: %v", err)
	}
	img, src, err := resolveRuntimeImage(root, "artem_two", "artem_two", "dev", "")
	if err != nil {
		t.Fatalf("resolveRuntimeImage: %v", err)
	}
	t.Logf("image=%s source=%s", img, src)
}

func TestZZReproFullArgs(t *testing.T) {
	root := `C:\Users\User\a_projects\hermes-hub\spaces\artem_two`
	settings, _ := stack.ReadEnvironment(root, "dev")
	svc := stack.RuntimeService(settings, "", root)
	if envMap, ok := svc["environment"].(stack.M); ok {
		for k, v := range envMap {
			t.Logf("env %s=%v", k, v)
		}
	}
}

func TestZZDumpArgs(t *testing.T) {
	abs, _ := filepath.Abs(`..\..\spaces\artem_two`)
	spacesRoot := filepath.Dir(abs)
	m := &Manager{cfg: Config{SpacesRoot: spacesRoot, Environment: "dev", RuntimePort: 8080, PIDs: 512, Memory: "2g", CPU: "2"}}
	settings, err := stack.ReadEnvironment(abs, "dev")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{PrincipalID: "artem_two", ContextID: "artem_two", RuntimeID: "artem_two", UserID: "artem_two", OrganizationID: "personal", PolicyVersion: "x", ContextRoot: abs, EnvFile: filepath.Join(abs, "runtime.auth"), ScopeID: "user:artem_two"}
	args, err := m.managedRunArgs(binding, settings, "zz-dump", "gen-1")
	if err != nil {
		t.Fatalf("managedRunArgs: %v", err)
	}
	out, _ := json.Marshal(args)
	t.Logf("ARGS %s", string(out))
}

func TestZZWriteArgs(t *testing.T) {
	abs, _ := filepath.Abs(`..\..\spaces\artem_two`)
	spacesRoot := filepath.Dir(abs)
	m := &Manager{cfg: Config{SpacesRoot: spacesRoot, Environment: "dev", RuntimePort: 8080, PIDs: 512, Memory: "2g", CPU: "2"}}
	settings, _ := stack.ReadEnvironment(abs, "dev")
	binding := Binding{PrincipalID: "artem_two", ContextID: "artem_two", RuntimeID: "artem_two", UserID: "artem_two", OrganizationID: "personal", PolicyVersion: "x", ContextRoot: abs, EnvFile: filepath.Join(abs, "runtime.auth"), ScopeID: "user:artem_two"}
	args, err := m.managedRunArgs(binding, settings, "zz-dump2", "gen-1")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, a := range args {
		a = strings.ReplaceAll(a, `C:\Users\User\a_projects\hermes-hub\`, "C:/Users/User/a_projects/hermes-hub/")
		a = strings.ReplaceAll(a, `\`, "/")
		b.WriteString(a + "\n")
	}
	if err := os.WriteFile(`..\..\.work\tmp\spawn-args.txt`, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}
