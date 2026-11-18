package toolhub

import (
	"os"
	"path/filepath"
	"testing"
)

// siteInitSeed copies the reviewed Linux cellinit bridge into the state
// root with a staged write + chmod + rename; non-ELF inputs are refused.
func TestSiteInitSeed(t *testing.T) {
	state := t.TempDir()
	// Not an ELF → refused before touching the state root.
	pe := filepath.Join(t.TempDir(), "bridge.bin")
	if err := os.WriteFile(pe, []byte("MZ90\x00\x03"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &genericController{config: GenericControllerConfig{HostingCellInit: pe, StateRoot: state}}
	if err := c.siteInitSeed(); err == nil {
		t.Fatal("PE binary accepted as Linux bridge")
	}
	// Valid ELF seeds hosting-cellinit.
	elf := filepath.Join(t.TempDir(), "cellinit")
	if err := os.WriteFile(elf, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...), 0600); err != nil {
		t.Fatal(err)
	}
	c.config.HostingCellInit = elf
	if err := c.siteInitSeed(); err != nil {
		t.Fatalf("siteInitSeed: %v", err)
	}
	info, err := os.Stat(filepath.Join(state, "hosting-cellinit"))
	if err != nil || info.Size() != 68 {
		t.Fatalf("seeded bridge: %v", err)
	}
}
