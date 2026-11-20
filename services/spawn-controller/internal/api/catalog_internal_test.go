package api

import (
	"errors"
	"reflect"
	"testing"

	"github.com/letya999/hermes-hub/internal/catalog"
)

// A failed load is not cached: the first call fails, the second retries
// and succeeds, and later calls serve the cached docs without loading
// again.
func TestCatalogLoadRetriesAfterFailure(t *testing.T) {
	calls := 0
	want := catalog.Docs{Toolsets: catalog.ToolsetsDoc{}}
	loader := catalogLoader(func() (catalog.Docs, error) {
		calls++
		if calls == 1 {
			return catalog.Docs{}, errors.New("transient catalog failure")
		}
		return want, nil
	})
	if _, err := loader(); err == nil {
		t.Fatal("first load should fail")
	}
	if got, err := loader(); err != nil {
		t.Fatalf("retry after failure: %v", err)
	} else if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry returned %+v, want %+v", got, want)
	}
	if _, err := loader(); err != nil {
		t.Fatalf("cached load: %v", err)
	}
	if calls != 2 {
		t.Fatalf("load called %d times, want 2 (fail once, succeed once)", calls)
	}
}
