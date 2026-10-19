package routines

import (
	"errors"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// watchRecipe is a reviewed probe preset. Expansion happens once at schedule
// create: the resolved fields are stored on the probe itself, so a later
// catalog change cannot silently alter an existing schedule (SPEC-0059).
type watchRecipe struct {
	Version int
	Extract []commodel.ExtractStep
}

var builtinRecipes = map[string]watchRecipe{
	// Canonical RSS/Atom feed watcher: feed → entry lines → canonical text.
	"rss_feed": {Version: 1, Extract: []commodel.ExtractStep{{Kind: "rss"}, {Kind: "strip"}}},
}

// ExpandRecipe resolves probe.Recipe into concrete probe fields. The
// resolved version is snapshotted onto the probe.
func ExpandRecipe(probe *commodel.WatchProbe) error {
	if probe.Recipe == "" {
		return nil
	}
	r, ok := builtinRecipes[probe.Recipe]
	if !ok {
		return errors.New("unknown probe recipe")
	}
	if len(probe.Extract) == 0 {
		probe.Extract = append([]commodel.ExtractStep(nil), r.Extract...)
	}
	probe.RecipeVersion = r.Version
	return nil
}
