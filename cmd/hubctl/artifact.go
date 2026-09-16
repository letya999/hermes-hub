package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func runArtifact(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("artifact requires import, preflight, approve or linux-bridge")
	}
	switch args[0] {
	case "import":
		return runArtifactImport(ctx, args[1:])
	case "preflight":
		return runArtifactPreflight(ctx, args[1:])
	case "approve":
		return runArtifactApprove(args[1:])
	case "linux-bridge":
		return runArtifactLinuxBridge(args[1:])
	default:
		return fmt.Errorf("unknown artifact command %q", args[0])
	}
}

func runArtifactImport(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("artifact import", flag.ContinueOnError)
	repository := f.String("repository", "", "canonical public GitHub repository")
	commit := f.String("commit", "", "exact lowercase commit SHA")
	configPath := f.String("config", "", "JSON ArtifactImportConfig (tools/effects/env/policy)")
	seccomp := f.String("seccomp", "docker/seccomp-buildkit-rootless.json", "pinned BuildKit seccomp profile")
	artifacts := f.String("artifacts", "", "absolute quarantine directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *repository == "" || *commit == "" || *configPath == "" || *artifacts == "" || f.NArg() != 0 {
		return fmt.Errorf("artifact import requires --repository, --commit, --config and --artifacts")
	}
	configBytes, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	var config toolhub.ArtifactImportConfig
	decoder := json.NewDecoder(strings.NewReader(string(configBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("artifact import config: %w", err)
	}
	artifactDirectory, err := filepath.Abs(*artifacts)
	if err != nil {
		return err
	}
	seccompPath, err := filepath.Abs(*seccomp)
	if err != nil {
		return err
	}
	packet, err := toolhub.ImportGitHubArtifact(ctx, toolhub.ArtifactSource{Repository: *repository, CommitSHA: *commit}, config, toolhub.RestrictedBuildConfig{SeccompPath: seccompPath, ArtifactDirectory: artifactDirectory, MaxArtifactBytes: 8 << 30})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(packet)
}

func runArtifactPreflight(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("artifact preflight", flag.ContinueOnError)
	packetPath := f.String("packet", "", "JSON packet returned by artifact import")
	artifacts := f.String("artifacts", "", "absolute quarantine directory")
	contractPath := f.String("contract", "", "output confirmed tool contract JSON")
	outputPacket := f.String("output-packet", "", "output restamped import packet")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *packetPath == "" || *artifacts == "" || *contractPath == "" || *outputPacket == "" || f.NArg() != 0 {
		return fmt.Errorf("artifact preflight requires --packet, --artifacts, --contract and --output-packet")
	}
	body, err := os.ReadFile(*packetPath)
	if err != nil {
		return err
	}
	var packet toolhub.ImportedArtifact
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&packet); err != nil {
		return fmt.Errorf("artifact packet: %w", err)
	}
	artifactDirectory, err := filepath.Abs(*artifacts)
	if err != nil {
		return err
	}
	preflighted, contract, err := toolhub.PreflightImportedArtifact(ctx, packet, artifactDirectory)
	if err != nil {
		return err
	}
	if err := writeJSONFile(*contractPath, contract); err != nil {
		return err
	}
	if err := writeJSONFile(*outputPacket, preflighted); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(contract)
}

func writeJSONFile(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func runArtifactApprove(args []string) error {
	f := flag.NewFlagSet("artifact approve", flag.ContinueOnError)
	packetPath := f.String("packet", "", "JSON packet returned by artifact import")
	storePath := f.String("store", "", "absolute ToolHub store path")
	review := f.String("review-digest", "", "exact review digest from the packet")
	contractPath := f.String("contract", "", "confirmed tool contract JSON from a real MCP tools/list or review manifest")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *packetPath == "" || *storePath == "" || *review == "" || *contractPath == "" || f.NArg() != 0 {
		return fmt.Errorf("artifact approve requires --packet, --store, --review-digest and --contract")
	}
	body, err := os.ReadFile(*packetPath)
	if err != nil {
		return err
	}
	var packet toolhub.ImportedArtifact
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&packet); err != nil {
		return fmt.Errorf("artifact packet: %w", err)
	}
	if packet.Definition.Source.ReviewDigest != *review {
		return fmt.Errorf("artifact review digest mismatch")
	}
	contractBody, err := os.ReadFile(*contractPath)
	if err != nil {
		return err
	}
	var contract toolhub.ConfirmedToolContract
	contractDecoder := json.NewDecoder(strings.NewReader(string(contractBody)))
	contractDecoder.DisallowUnknownFields()
	if err := contractDecoder.Decode(&contract); err != nil {
		return fmt.Errorf("artifact tool contract: %w", err)
	}
	if err := toolhub.AttachConfirmedToolContract(&packet, contract); err != nil {
		return err
	}
	store, err := toolhub.LoadStaged(*storePath)
	if err != nil {
		if os.IsNotExist(err) {
			store = toolhub.NewStore()
		} else {
			return err
		}
	}
	if err := store.RegisterTrustedArtifact(packet, *review); err != nil {
		return err
	}
	return store.Save(*storePath)
}

func runArtifactLinuxBridge(args []string) error {
	f := flag.NewFlagSet("artifact linux-bridge", flag.ContinueOnError)
	output := f.String("output", "", "absolute Linux ELF hubctl path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *output == "" || f.NArg() != 0 {
		return fmt.Errorf("artifact linux-bridge requires --output")
	}
	return toolhub.BuildLinuxCompanionBridge(*output)
}
