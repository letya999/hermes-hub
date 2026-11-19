package main

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/shared/locale"
)

func TestHubServeZoneDispatch(t *testing.T) {
	for _, lang := range locale.All {
		t.Setenv("HUB_DEFAULT_LANG", string(lang))
		for _, args := range [][]string{nil, {"serve"}, {"serve", "--zone"}, {"serve", "--zone", "unknown"}, {"serve", "--zone=control,agent"}, {"serve", "--zone=agent", "--zone=edge"}} {
			err := run(args)
			if err == nil {
				t.Fatalf("accepted %v", args)
			}
			want := locale.T(lang, "hub.usage")
			if len(args) == 3 && args[1] == "--zone=agent" {
				want = locale.T(lang, "hub.agent-usage")
			}
			if err.Error() != want {
				t.Fatalf("%v: %v, want %q", args, err, want)
			}
		}
	}
	t.Setenv("HUB_DEFAULT_LANG", "en")
	for _, zone := range []string{"agent", "edge", "control"} {
		err := run([]string{"serve", "--zone", zone, "--unexpected"})
		if err == nil || strings.Contains(err.Error(), "hub serve") {
			t.Fatalf("zone %s did not reach its entrypoint: %v", zone, err)
		}
	}
}
