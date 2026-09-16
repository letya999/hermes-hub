package toolhub

import (
	"fmt"
	"slices"
	"strings"
)

// RuntimeSecurityProfile is the pre-start contract a Docker/ToolHive adapter
// must prove before it launches an artifact. It is deliberately data-only so
// a caller cannot smuggle arbitrary Docker flags through the catalog.
type RuntimeSecurityProfile struct {
	User            string
	NetworkMode     string
	PIDMode         string
	ReadonlyRootfs  bool
	Privileged      bool
	NoNewPrivileges bool
	CapDrop         []string
	CapAdd          []string
	HostMounts      []string
	CPUQuota        int64
	MemoryBytes     int64
	PIDsLimit       int64
	TimeoutSeconds  int
	OutputBytes     int64
	SeccompProfile  string
	AppArmorProfile string
}

func (p RuntimeSecurityProfile) Validate(definition ToolDefinition) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if p.User == "" || p.User == "0" || p.User == "0:0" || p.NetworkMode == "" || p.NetworkMode == "host" || p.PIDMode != "" || !p.ReadonlyRootfs || p.Privileged || !p.NoNewPrivileges || len(p.CapAdd) != 0 || len(p.CapDrop) != 1 || p.CapDrop[0] != "ALL" || len(p.HostMounts) != 0 || p.CPUQuota < 1 || p.MemoryBytes < 16<<20 || p.PIDsLimit < 1 || p.TimeoutSeconds < 1 || p.OutputBytes < 1 || p.SeccompProfile == "" || strings.Contains(strings.ToLower(p.SeccompProfile), "unconfined") || strings.Contains(strings.ToLower(p.AppArmorProfile), "unconfined") {
		return fmt.Errorf("%w: pre-start runtime security profile", ErrIsolation)
	}
	if p.TimeoutSeconds > definition.Execution.TimeoutSeconds || p.OutputBytes > int64(definition.Execution.OutputBytes) || p.CPUQuota > int64(definition.Execution.CPUMillis)*1_000_000 || p.MemoryBytes > int64(definition.Execution.MemoryMiB)*1_048_576 || p.PIDsLimit > int64(definition.Execution.MaxPIDs) {
		return fmt.Errorf("%w: runtime limits exceed definition", ErrIsolation)
	}
	if strings.ContainsAny(p.SeccompProfile, "\r\n") || strings.ContainsAny(p.AppArmorProfile, "\r\n") {
		return fmt.Errorf("%w: invalid security profile name", ErrInvalid)
	}
	for _, mount := range p.HostMounts {
		if mount != "" {
			return fmt.Errorf("%w: host mount denied", ErrIsolation)
		}
	}
	return nil
}

func (p RuntimeSecurityProfile) Capabilities() []string {
	return slices.Clone(p.CapDrop)
}
