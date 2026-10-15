package api

import (
	"errors"
	"strings"
	"testing"
)

func TestRuntimeSecurityProfileContract(t *testing.T) {
	def := readyDefinition("sha256:" + strings.Repeat("ab", 32))
	good := RuntimeSecurityProfile{
		User: "65534:65534", NetworkMode: "bridge", ReadonlyRootfs: true, NoNewPrivileges: true,
		CapDrop: []string{"ALL"}, CPUQuota: 100_000_000, MemoryBytes: 64 << 20, PIDsLimit: 4,
		TimeoutSeconds: 10, OutputBytes: 1024, SeccompProfile: "default", AppArmorProfile: "docker-default",
	}
	if err := good.Validate(def); err != nil {
		t.Fatalf("good profile: %v", err)
	}
	if got := good.Capabilities(); len(got) != 1 || got[0] != "ALL" {
		t.Fatalf("capabilities: %v", got)
	}
	// every hardened knob is required: root user, host net/pid, writable root,
	// privilege, added caps, host mounts, missing limits or unconfined profiles
	cases := map[string]func(*RuntimeSecurityProfile){
		"root user":        func(p *RuntimeSecurityProfile) { p.User = "0:0" },
		"empty user":       func(p *RuntimeSecurityProfile) { p.User = "" },
		"host network":     func(p *RuntimeSecurityProfile) { p.NetworkMode = "host" },
		"host pid":         func(p *RuntimeSecurityProfile) { p.PIDMode = "host" },
		"writable rootfs":  func(p *RuntimeSecurityProfile) { p.ReadonlyRootfs = false },
		"privileged":       func(p *RuntimeSecurityProfile) { p.Privileged = true },
		"added cap":        func(p *RuntimeSecurityProfile) { p.CapAdd = []string{"SYS_ADMIN"} },
		"host mount":       func(p *RuntimeSecurityProfile) { p.HostMounts = []string{"/var/run/docker.sock"} },
		"no timeout":       func(p *RuntimeSecurityProfile) { p.TimeoutSeconds = 0 },
		"unconfined":       func(p *RuntimeSecurityProfile) { p.SeccompProfile = "unconfined" },
		"timeout over":     func(p *RuntimeSecurityProfile) { p.TimeoutSeconds = def.Execution.TimeoutSeconds + 1 },
		"memory over":      func(p *RuntimeSecurityProfile) { p.MemoryBytes = int64(def.Execution.MemoryMiB)*1048576 + 1 },
		"profile newline":  func(p *RuntimeSecurityProfile) { p.SeccompProfile = "x\ny" },
		"low memory":       func(p *RuntimeSecurityProfile) { p.MemoryBytes = 8 << 20 },
		"missing cap drop": func(p *RuntimeSecurityProfile) { p.CapDrop = nil },
	}
	for name, mutate := range cases {
		profile := good
		mutate(&profile)
		if err := profile.Validate(def); !errors.Is(err, ErrIsolation) && !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
