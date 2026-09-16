package toolhub

import "testing"

func TestRuntimeSecurityProfileFailsClosed(t *testing.T) {
	d := statefulContainerDefinition()
	base := RuntimeSecurityProfile{User: "10001:10001", NetworkMode: "hermes-work", ReadonlyRootfs: true, NoNewPrivileges: true, CapDrop: []string{"ALL"}, CPUQuota: 1_000_000_000, MemoryBytes: 512 << 20, PIDsLimit: 64, TimeoutSeconds: 60, OutputBytes: 2 << 20, SeccompProfile: "mcp-default.json"}
	if err := base.Validate(d); err != nil {
		t.Fatal(err)
	}
	if caps := base.Capabilities(); len(caps) != 1 || caps[0] != "ALL" {
		t.Fatalf("capability snapshot=%v", caps)
	}
	for name, mutate := range map[string]func(*RuntimeSecurityProfile){
		"privileged":         func(p *RuntimeSecurityProfile) { p.Privileged = true },
		"host-network":       func(p *RuntimeSecurityProfile) { p.NetworkMode = "host" },
		"host-pid":           func(p *RuntimeSecurityProfile) { p.PIDMode = "host" },
		"writable-root":      func(p *RuntimeSecurityProfile) { p.ReadonlyRootfs = false },
		"capability":         func(p *RuntimeSecurityProfile) { p.CapAdd = []string{"NET_ADMIN"} },
		"unbounded":          func(p *RuntimeSecurityProfile) { p.MemoryBytes = 8 << 30 },
		"mount":              func(p *RuntimeSecurityProfile) { p.HostMounts = []string{"C:\\Users"} },
		"unconfined-seccomp": func(p *RuntimeSecurityProfile) { p.SeccompProfile = "unconfined" },
		"builder-bootstrap":  func(p *RuntimeSecurityProfile) { p.CapAdd = []string{"SETUID"} },
	} {
		candidate := base
		mutate(&candidate)
		if err := candidate.Validate(d); err == nil {
			t.Fatalf("%s profile accepted", name)
		}
	}
	badName := base
	badName.SeccompProfile = "bad\nprofile"
	if err := badName.Validate(d); err == nil {
		t.Fatal("newline security profile accepted")
	}
}
