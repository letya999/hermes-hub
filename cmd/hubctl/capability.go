package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

// This command is a host operation, never an agent-facing MCP method. Its input
// is an operator-reviewed record; the protected store checks revisions and pins.
func runCapability(args []string) error {
	f := flag.NewFlagSet("capability", flag.ContinueOnError)
	kind := f.String("kind", "", "policy or profile")
	file := f.String("file", "", "operator-reviewed JSON record")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "protected ToolHub registry path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*kind != "policy" && *kind != "profile") || *file == "" || *storePath == "" {
		return errors.New("capability requires --kind policy|profile, --file and --toolhub-store; no positional arguments")
	}
	absolute, err := filepath.Abs(*storePath)
	if err != nil {
		return err
	}
	store, err := toolhub.Load(absolute)
	create := errors.Is(err, os.ErrNotExist)
	if err != nil && !create {
		return err
	}
	if create {
		store = toolhub.NewStore()
	}
	var id string
	var revision uint64
	var status toolhub.Status
	if *kind == "policy" {
		var record toolhub.CapabilityPolicy
		if err := readCapabilityRecord(*file, &record); err != nil {
			return err
		}
		if err := store.PutCapabilityPolicy(record); err != nil {
			return err
		}
		id, revision, status = record.PolicyID, record.Revision, record.Status
	} else {
		var record toolhub.CapabilityProfile
		if err := readCapabilityRecord(*file, &record); err != nil {
			return err
		}
		if err := store.PutCapabilityProfile(record); err != nil {
			return err
		}
		id, revision, status = record.ProfileID, record.Revision, record.Status
	}
	if create {
		if err := store.Save(absolute); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": *kind, "id": id, "revision": revision, "status": status})
}

func readCapabilityRecord(path string, record any) error {
	f, err := os.Open(path) // #nosec G304 -- explicitly supplied operator review file; output contains IDs only.
	if err != nil {
		return err
	}
	defer f.Close()
	const maxRecordBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxRecordBytes {
		return errors.New("capability record exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(record); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("exactly one capability JSON record required")
	}
	return nil
}
