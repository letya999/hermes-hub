package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericProxyConfigContract(t *testing.T) {
	cfg, err := GenericProxyConfig([]string{"api.slack.com:443", "10.0.0.0/8", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg, "dstdomain .api.slack.com") ||
		!strings.Contains(cfg, "acl hermes_1 dst 10.0.0.0/8") ||
		!strings.Contains(cfg, "port 443") ||
		!strings.Contains(cfg, "http_access deny all") {
		t.Fatalf("config:\n%s", cfg)
	}
	if _, err := GenericProxyConfig(nil); err != ErrIsolation {
		t.Fatalf("empty egress: %v", err)
	}
	if _, err := GenericProxyConfig(make([]string, 33)); err != ErrIsolation {
		t.Fatalf("oversize egress: %v", err)
	}
	if _, err := GenericProxyConfig([]string{"host\ninject"}); err != ErrIsolation {
		t.Fatalf("injected host: %v", err)
	}
	if _, err := GenericProxyConfig([]string{"singlelabel"}); err != ErrIsolation {
		t.Fatalf("single-label host: %v", err)
	}
	cfg, err = GenericProxyConfig([]string{"localhost:8080"})
	if err != nil || !strings.Contains(cfg, "dstdomain localhost") {
		t.Fatalf("localhost egress: %v\n%s", err, cfg)
	}
}

func TestEnvSecretScannersAndEquality(t *testing.T) {
	if !EnvContainsForwardingSecret([]string{"HERMES_BRIDGE_TOKEN=x"}) ||
		!EnvContainsForwardingSecret([]string{"TOOLHIVE_SECRET_VALUE=1"}) ||
		!EnvContainsForwardingSecret([]string{"bridge_auth=y"}) {
		t.Fatal("forwarding secret not detected")
	}
	if EnvContainsForwardingSecret([]string{"PATH=/usr/bin"}) {
		t.Fatal("false positive forwarding secret")
	}
	long := strings.Repeat("k", 32)
	if !EnvHasBridgeToken([]string{"HERMES_BRIDGE_TOKEN=" + long}) {
		t.Fatal("bridge token not detected")
	}
	if EnvHasBridgeToken([]string{"HERMES_BRIDGE_TOKEN=short"}) {
		t.Fatal("short bridge token accepted")
	}
	if !EqualStrings([]string{"a", "b"}, []string{"a", "b"}) || EqualStrings([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("EqualStrings wrong")
	}
	exec := ExecutionPolicy{TimeoutSeconds: 5, Egress: []string{"h"}, TCPForwards: []TCPForward{{Listen: 1, TargetEnv: "H"}}, Mounts: []Mount{{Target: "/t"}}}
	if !ReflectExecution(exec, exec) {
		t.Fatal("ReflectExecution reflexivity")
	}
	other := exec
	other.TCPForwards = []TCPForward{{Listen: 9, TargetEnv: "H"}}
	if ReflectExecution(exec, other) {
		t.Fatal("ReflectExecution ignored forward diff")
	}
	other = exec
	other.Mounts = []Mount{{Target: "/other"}}
	if ReflectExecution(exec, other) {
		t.Fatal("ReflectExecution ignored mount diff")
	}
	if !Contains([]string{"a"}, "a") || Contains([]string{"a"}, "b") {
		t.Fatal("Contains wrong")
	}
	env := IsolatedToolHiveEnviron(map[string]string{"CUSTOM_KEY": "v", "path": "override"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CUSTOM_KEY=v") || !strings.Contains(joined, "path=override") {
		t.Fatalf("extra env missing: %v", env)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			t.Fatalf("override leaked ambient PATH into: %v", env)
		}
	}
}

func TestReadGenericSecretsContract(t *testing.T) {
	if got, err := ReadGenericSecrets("", ToolDefinition{}); got != nil || err != nil {
		t.Fatalf("empty path: %v %v", got, err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "secrets.txt")
	def := ToolDefinition{
		Environment: []string{"PLAIN_VAR"},
		Credentials: []CredentialInput{{Name: "API_TOKEN", Required: true}},
	}
	if _, err := ReadGenericSecrets(bad, def); !errors.Is(err, ErrIsolation) {
		t.Fatalf("non-env extension: %v", err)
	}
	good := filepath.Join(dir, "secrets.env")
	if err := os.WriteFile(good, []byte("API_TOKEN=tok\nPLAIN_VAR=v\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := ReadGenericSecrets(good, def)
	if err != nil || values["API_TOKEN"] != "tok" || values["PLAIN_VAR"] != "v" {
		t.Fatalf("secrets: %v %v", values, err)
	}
	for name, body := range map[string]string{
		"undeclared":  "UNDECLARED=x\nAPI_TOKEN=t\n",
		"empty-value": "API_TOKEN=\n",
		"no-equals":   "API_TOKEN\n",
		"dup":         "API_TOKEN=a\nAPI_TOKEN=b\n",
		"missing-req": "PLAIN_VAR=v\n",
		"crlf-value":  "API_TOKEN=a\rb\n",
	} {
		path := filepath.Join(dir, name+".env")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadGenericSecrets(path, def); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := ReadGenericSecrets(filepath.Join(dir, "ghost.env"), def); err == nil {
		t.Fatal("missing file read")
	}
}

func TestSecurityOptHasSeccompContract(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "seccomp.json")
	if err := os.WriteFile(profile, []byte(`{"defaultAction":"SCMP_ACT_ERRNO"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A seccomp option pointing at the profile path itself is a match.
	if !SecurityOptHasSeccomp([]string{"seccomp=" + profile}, profile) {
		t.Fatal("direct profile-path reference not detected")
	}
	// An inline JSON profile is a match only when it equals the file content.
	if !SecurityOptHasSeccomp([]string{`seccomp={"defaultAction":"SCMP_ACT_ERRNO"}`}, profile) {
		t.Fatal("inline JSON profile not matched")
	}
	if SecurityOptHasSeccomp([]string{`seccomp={"defaultAction":"SCMP_ACT_ALLOW"}`}, profile) {
		t.Fatal("different profile matched")
	}
	if SecurityOptHasSeccomp([]string{"seccomp=not-json"}, profile) {
		t.Fatal("invalid JSON matched")
	}
	if SecurityOptHasSeccomp([]string{"seccomp=" + profile}, filepath.Join(dir, "ghost.json")) {
		t.Fatal("missing profile matched")
	}
}
