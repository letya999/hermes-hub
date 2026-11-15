package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

var loadOperatorStore = toolhub.Load

func retryableStoreConflict(err error) bool {
	return errors.Is(err, api.ErrConflict)
}

// Host-only catalog binding. No credentials or model-selected identity.
func runCatalog(args []string) error {
	f := flag.NewFlagSet("catalog-enable", flag.ContinueOnError)
	storePath := f.String("toolhub-store", "", "absolute protected registry path")
	definition := f.String("definition", "", "exact catalog definition id")
	version := f.String("version", "", "exact immutable definition version")
	principal := f.String("principal", "", "target principal")
	contextID := f.String("context", "", "target context (defaults to principal)")
	runtimeID := f.String("runtime", "", "target runtime (defaults to principal)")
	policy := f.String("policy-version", "", "runtime policy version")
	profile := f.String("profile", "", "managed profile id; omit for unmanaged")
	environment := f.String("env", "dev", "managed environment")
	generation := f.Uint64("generation", 1, "managed runtime generation")
	issuer := f.String("issuer", "operator", "host operator")
	extend := f.Bool("extend-ceiling", false, "explicitly add this catalog's tuples to the policy ceiling")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !filepath.IsAbs(*storePath) || *definition == "" || *version == "" {
		return fmt.Errorf("catalog-enable requires absolute --toolhub-store, --definition and --version")
	}
	if *contextID == "" {
		*contextID = *principal
	}
	if *runtimeID == "" {
		*runtimeID = *principal
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: *principal, ExternalIdentityID: *principal,
		ContextID: *contextID, RuntimeID: *runtimeID, ConversationID: "catalog", DeliveryTargetID: "catalog", PolicyVersion: *policy}
	if *profile != "" {
		auth.CapabilityProfile, auth.Environment, auth.Generation = *profile, *environment, *generation
	}
	for attempt := 0; attempt < 3; attempt++ {
		store, err := loadOperatorStore(*storePath)
		if err != nil {
			return err
		}
		binding, err := store.EnableCatalog(auth, *definition, *version, *issuer, *extend)
		_ = store.CloseDB()
		if err == nil {
			return json.NewEncoder(os.Stdout).Encode(binding)
		}
		if !retryableStoreConflict(err) || attempt == 2 {
			return err
		}
	}
	return api.ErrConflict
}
