package api

import (
	"strings"
	"testing"
)

func TestUnsafeRunOptionsAndBuildCache(t *testing.T) {
	// only cache mounts, default/none networks and sandbox security are metadata
	if unsafeRunOptions([]string{"--mount=type=cache,target=/x"}) {
		t.Fatal("cache mount unsafe")
	}
	if unsafeRunOptions([]string{"--mount=type=bind,target=/x"}) != true {
		t.Fatal("bind mount allowed")
	}
	if unsafeRunOptions([]string{"--network=host"}) != true || unsafeRunOptions([]string{"--network=none"}) {
		t.Fatal("network options")
	}
	if unsafeRunOptions([]string{"--security=insecure"}) != true || unsafeRunOptions([]string{"--security=sandbox"}) {
		t.Fatal("security options")
	}
	if unsafeRunOptions([]string{"--device=/dev/x"}) != true {
		t.Fatal("unknown option allowed")
	}
	// first non-flag token ends option scanning
	if unsafeRunOptions([]string{"run", "--mount=type=bind"}) {
		t.Fatal("option scan continued past command")
	}
	// cache mounts need type=cache exactly
	if cacheOnlyMount("type=bind") || !cacheOnlyMount("id=x,type=cache") {
		t.Fatal("cacheOnlyMount")
	}

	// build cache prefixes only exist with the env opt-in
	t.Setenv("HUB_BUILD_CACHE", "")
	for _, lang := range []string{"go", "node", "python", "rust"} {
		if buildCacheRunPrefix(lang) != "" {
			t.Fatalf("%s prefix without env", lang)
		}
	}
	t.Setenv("HUB_BUILD_CACHE", "1")
	for _, lang := range []string{"go", "node", "python", "rust"} {
		if !strings.Contains(buildCacheRunPrefix(lang), "--mount=type=cache") {
			t.Fatalf("%s prefix missing", lang)
		}
	}
	if buildCacheRunPrefix("ruby") != "" {
		t.Fatal("unknown language cached")
	}
}
