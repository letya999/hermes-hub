package toolhub

import (
	"os"
	"testing"
)

// Historical page assertions across this package expect the Russian literals
// the loopback UI used to hardcode; pinning the fallback keeps them stable
// while request-driven ?lang=/Accept-Language resolution is tested explicitly.
func TestMain(m *testing.M) {
	_ = os.Setenv("HUB_DEFAULT_LANG", "ru")
	os.Exit(m.Run())
}
