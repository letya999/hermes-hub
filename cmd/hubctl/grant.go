package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/toolhub"
)

func runGrant(_ context.Context, args []string) error {
	f := flag.NewFlagSet("grant", flag.ContinueOnError)
	profile := f.String("user", "me", "principal receiving the grant")
	kind := f.String("kind", "", "catalog-default, definition or self-install")
	definition := f.String("definition", "", "definition id for a definition grant")
	version := f.String("version", "", "definition version for a definition grant")
	storePath := f.String("toolhub-store", os.Getenv("HUB_TOOLHUB_STORE"), "ToolHub registry path")
	dir := f.String("dir", "", "private deployment directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *kind == "" {
		return fmt.Errorf("grant requires --kind")
	}
	if *dir == "" {
		*dir = filepath.Join("spaces", *profile)
	}
	if *storePath == "" {
		absDir, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		*storePath = filepath.Join(absDir, "runtime", "toolhub", "store.json")
	}
	if !filepath.IsAbs(*storePath) {
		abs, err := filepath.Abs(*storePath)
		if err != nil {
			return err
		}
		*storePath = abs
	}
	store, err := toolhub.Load(*storePath)
	if err != nil {
		if _, statErr := os.Stat(*storePath); statErr == nil {
			return err
		}
		store = toolhub.NewStore()
	}
	grant := toolhub.OperatorGrant(toolhub.GrantKind(*kind), *profile, *definition, *version)
	if err := store.PutGrant(grant); err != nil {
		return err
	}
	if err := store.Save(*storePath); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"grant_id": grant.GrantID, "kind": string(grant.Kind), "principal_id": grant.PrincipalID, "status": string(grant.Status)})
}
