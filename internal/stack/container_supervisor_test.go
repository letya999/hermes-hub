package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
)

func TestComposeContainerSupervisorIsSinglePrivateControlService(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	c := Compose(s, root, dir)
	services := c["services"].(M)
	svc, ok := services["supervisor"].(M)
	if !ok {
		t.Fatal("missing container supervisor")
	}
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("resident user runtime")
	}
	// The control endpoint may publish on loopback only — the operator reaches
	// apply/respawn from the host where container IPs are not routable (Docker
	// Desktop, WSL); any non-loopback publish exposes the docker-socket owner.
	if ports, ok := svc["ports"]; ok {
		if !reflect.DeepEqual(ports, []string{"127.0.0.1:8876:8876"}) {
			t.Fatalf("supervisor publishes beyond loopback: %v", ports)
		}
	}
	if svc["image"] != services["toolhub"].(M)["image"] || svc["read_only"] != true || svc["restart"] != "unless-stopped" {
		t.Fatalf("supervisor contract: %#v", svc)
	}
	if svc["user"] != fmt.Sprintf("0:%d", max(0, os.Getgid())) {
		t.Fatal("supervisor cannot maintain operator-owned bind permissions")
	}
	if !reflect.DeepEqual(svc["cap_drop"], []string{"ALL"}) || !reflect.DeepEqual(svc["cap_add"], []string{"SETUID", "SETGID", "SETPCAP", "FOWNER"}) ||
		!reflect.DeepEqual(svc["security_opt"], []string{"no-new-privileges:true"}) {
		t.Fatal("supervisor bootstrap capabilities changed")
	}
	entrypoint := svc["entrypoint"].([]string)
	dockerGroup := fmt.Sprint(dockerSocketGID("/var/run/docker.sock"))
	if !reflect.DeepEqual(entrypoint[:9], []string{"/usr/bin/setpriv", "--reuid=10001", fmt.Sprintf("--regid=%d", max(0, os.Getgid())), "--groups", dockerGroup, "--inh-caps=+fowner", "--ambient-caps=+fowner", "--bounding-set=-all,+fowner", "hubctl"}) {
		t.Fatal("supervisor must drop uid and all capabilities except FOWNER before starting")
	}
	if services["toolhub"].(M)["user"] != fmt.Sprintf("10001:%d", max(0, os.Getgid())) {
		t.Fatal("operator uid leaked to ToolHub")
	}
	// ToolHub always joins the operator file group; the socket group is added
	// only when it differs — the controller runs socket-group only.
	wantToolhub := []string{fmt.Sprint(max(0, os.Getgid()))}
	if dockerGroup != wantToolhub[0] {
		wantToolhub = append(wantToolhub, dockerGroup)
	}
	if !reflect.DeepEqual(services["toolhub"].(M)["group_add"], wantToolhub) {
		t.Fatalf("toolhub group_add = %v, want %v", services["toolhub"].(M)["group_add"], wantToolhub)
	}
	if !reflect.DeepEqual(services["workload-controller"].(M)["group_add"], []string{dockerGroup}) {
		t.Fatalf("workload-controller lacks Docker socket group: %v", services["workload-controller"].(M)["group_add"])
	}
	if !reflect.DeepEqual(svc["networks"], []string{"default", sharedNetworkName, "supervisor-control"}) {
		t.Fatal(svc["networks"])
	}
	if c["networks"].(M)["supervisor-control"].(M)["internal"] != true {
		t.Fatal("control network not internal")
	}
	if svc["environment"].(M)["HUB_SUPERVISOR_HOST_SPACES"] != filepath.ToSlash(filepath.Dir(dir)) {
		t.Fatal("incorrect host bind root")
	}
	for _, raw := range svc["volumes"].([]any) {
		v := raw.(M)
		if v["target"] == "/src" {
			t.Fatal("source tree mounted")
		}
	}
	s.SupervisorURL = "http://host.docker.internal:8876"
	if _, ok := Compose(s, root, dir)["services"].(M)["supervisor"]; ok {
		t.Fatal("host selection spawned second supervisor")
	}
}

// The supervisor's opt-in authorities — administrative holds and the
// personal model — are read from its environment at startup. An operator
// provisioning them through compose env substitution must have them
// forwarded; unset they resolve empty and stay disabled.
func TestComposeSupervisorForwardsOptInAuthorities(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	env := Compose(s, t.TempDir(), dir)["services"].(M)["supervisor"].(M)["environment"].(M)
	for _, key := range []string{"HUB_SUPERVISOR_ADMIN_AUTH", "HUB_PERSONAL_MODEL", "HUB_MANAGED_MODEL_URL", "HUB_RUN_BUDGET", "HUB_STREAM_IDLE"} {
		if _, ok := env[key]; !ok {
			t.Fatalf("opt-in %s not forwarded to supervisor env", key)
		}
	}
	if _, ok := env["OPENAI_API_KEY"]; ok {
		t.Fatal("model caller credential projected to personal supervisor")
	}
}

func TestServiceAuthIsEnvironmentScopedAndProjectedOnlyToIssuers(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	legacyCaller := filepath.ToSlash(filepath.Join(dir, "model-relay-caller.auth"))
	if err := os.WriteFile(legacyCaller, []byte("synthetic legacy file"), 0600); err != nil {
		t.Fatal(err)
	}
	issuers := []struct {
		key, consumer string
		ensure        func(string, string) error
		path          func(string, string) string
		values        []string
	}{
		{key: contract.CommunicationServiceAuthEnv, consumer: "communication-hub", ensure: ensureCommunicationServiceAuth, path: communicationServiceAuthPath},
		{key: contract.DreamingServiceAuthEnv, consumer: "toolhub", ensure: ensureDreamingServiceAuth, path: dreamingServiceAuthPath},
	}
	for _, environment := range []string{"dev", "prod", "dev"} {
		s.Environment = environment
		services := Compose(s, root, dir)["services"].(M)
		for i := range issuers {
			issuer := &issuers[i]
			if err := issuer.ensure(dir, environment); err != nil {
				t.Fatal(err)
			}
			file := issuer.path(dir, environment)
			values, err := ReadSecrets(file)
			if err != nil || len(values[issuer.key]) != 64 {
				t.Fatal("service issuer credential not provisioned")
			}
			issuer.values = append(issuer.values, values[issuer.key])
			for name, raw := range services {
				service := raw.(M)
				envFiles, _ := service["env_file"].([]any)
				projected := slices.ContainsFunc(envFiles, func(entry any) bool {
					fileSpec, _ := entry.(M)
					return fileSpec["path"] == filepath.ToSlash(file)
				})
				if projected != (name == "supervisor" || name == issuer.consumer) {
					t.Errorf("unexpected %s issuer credential projection for %s/%s", issuer.consumer, environment, name)
				}
			}
		}
		toolhub := services["toolhub"].(M)
		for _, raw := range toolhub["volumes"].([]any) {
			mount, _ := raw.(M)
			if mount["source"] == legacyCaller {
				t.Fatal("obsolete model caller file mounted into ToolHub")
			}
		}
		for _, raw := range toolhub["env_file"].([]any) {
			if raw.(M)["path"] == filepath.ToSlash(filepath.Join(dir, "model-relay.auth")) {
				t.Fatal("ToolHub receives the combined model relay credentials")
			}
		}
	}
	for _, issuer := range issuers {
		if issuer.values[0] == issuer.values[1] || issuer.values[0] != issuer.values[2] || !GatewayOwnedSecret(issuer.key) {
			t.Fatalf("%s issuer secret is shared across environments, rotated during render, or exposed to runtime", issuer.consumer)
		}
	}
	if issuers[0].values[0] == issuers[1].values[0] {
		t.Fatal("the issuers share a bootstrap credential")
	}
}
