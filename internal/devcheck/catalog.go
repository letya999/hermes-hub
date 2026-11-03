package devcheck

import "github.com/letya999/hermes-hub/internal/catalog"

// CatalogEmit regenerates platform/catalogs/<Release>/ — the immutable
// M29 catalogs. Emit refuses to overwrite drifted bytes, so a vocabulary
// change must bump catalog.Release first.
func CatalogEmit(root string) error {
	return catalog.Emit(root)
}
