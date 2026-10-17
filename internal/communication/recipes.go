package communication

import "errors"

// watchRecipe is a reviewed probe preset. Expansion happens once at schedule
// create: the resolved fields are stored on the probe itself, so a later
// catalog change cannot silently alter an existing schedule (SPEC-0057).
type watchRecipe struct {
	Version int
	Extract []ExtractStep
}

var builtinRecipes = map[string]watchRecipe{
	// Canonical RSS/Atom feed watcher: feed → entry lines → canonical text.
	"rss_feed": {Version: 1, Extract: []ExtractStep{{Kind: "rss"}, {Kind: "strip"}}},
}

// expandRecipe resolves probe.Recipe into concrete probe fields. The
// resolved version is snapshotted onto the probe.
func expandRecipe(probe *WatchProbe) error {
	if probe.Recipe == "" {
		return nil
	}
	r, ok := builtinRecipes[probe.Recipe]
	if !ok {
		return errors.New("unknown probe recipe")
	}
	if len(probe.Extract) == 0 {
		probe.Extract = append([]ExtractStep(nil), r.Extract...)
	}
	probe.RecipeVersion = r.Version
	return nil
}
