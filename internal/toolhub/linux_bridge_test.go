package toolhub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLinuxBridgeBinaryRejectsWindowsAndNonELF(t *testing.T) {
	root := t.TempDir()
	elf := filepath.Join(root, "hubctl-linux")
	if err := os.WriteFile(elf, []byte(linuxELFMagic+"rest"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLinuxBridgeBinary(elf); err != nil {
		t.Fatalf("ELF bridge rejected: %v", err)
	}
	if _, err := resolveLinuxBridgeBinary(elf); err != nil {
		t.Fatalf("configured ELF bridge rejected: %v", err)
	}
	pe := filepath.Join(root, "hubctl.exe")
	if err := os.WriteFile(pe, []byte("MZ\x90\x00"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLinuxBridgeBinary(pe); err == nil {
		t.Fatal("Windows hubctl.exe accepted as a Linux bridge")
	}
	peNoExt := filepath.Join(root, "hubctl")
	if err := os.WriteFile(peNoExt, []byte("MZ"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLinuxBridgeBinary(peNoExt); err == nil {
		t.Fatal("PE magic accepted as a Linux bridge")
	}
	script := filepath.Join(root, "hubctl.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLinuxBridgeBinary(script); err == nil {
		t.Fatal("non-ELF script accepted as a Linux bridge")
	}
	if err := ValidateLinuxBridgeBinary("relative-hubctl"); err == nil {
		t.Fatal("relative bridge path accepted")
	}
	if err := BuildLinuxCompanionBridge(filepath.Join(root, "hubctl.exe")); err == nil {
		t.Fatal("Windows output path accepted for Linux bridge build")
	}
	if err := BuildLinuxCompanionBridge("linux-bridge"); err == nil {
		t.Fatal("relative linux bridge output accepted")
	}
}

func TestBuildLinuxCompanionBridgeProducesELF(t *testing.T) {
	output := filepath.Join(t.TempDir(), "hubctl-linux")
	if err := BuildLinuxCompanionBridge(output); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLinuxBridgeBinary(output); err != nil {
		t.Fatal(err)
	}
	if resolved, err := resolveLinuxBridgeBinary(output); err != nil || resolved != output {
		t.Fatalf("resolve built bridge: %q %v", resolved, err)
	}
	if linuxBridgeArch() != "amd64" && linuxBridgeArch() != "arm64" {
		t.Fatalf("unexpected linux bridge arch %q", linuxBridgeArch())
	}
	if _, err := resolveLinuxBridgeBinary(""); err == nil && filepath.Ext(os.Args[0]) == ".exe" {
		t.Fatal("empty bridge_binary accepted the Windows test executable")
	}
}

func TestResolveLinuxBridgeBinaryRequiresConfiguredPEReplacement(t *testing.T) {
	root := t.TempDir()
	pe := filepath.Join(root, "current")
	if err := os.WriteFile(pe, []byte("MZ payload"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLinuxBridgeBinary(pe); err == nil {
		t.Fatal("configured PE bridge accepted")
	}
}
