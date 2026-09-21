package toolhub

import "testing"

func TestDockerBindSource(t *testing.T) {
	cases := []struct {
		name string
		root string
		path string
		want string
	}{
		{"unset passes through", "", "/state/materialized/cred", "/state/materialized/cred"},
		{"exact prefix", "/state=/host/space/runtime", "/state", "/host/space/runtime"},
		{"nested path", "/state=/host/space/runtime", "/state/materialized/cred", "/host/space/runtime/materialized/cred"},
		{"prefix must match segment", "/state=/host/x", "/statex/other", "/statex/other"},
		{"first matching pair wins", "/a=/h/a,/state=/h/s", "/state/f", "/h/s/f"},
		{"malformed pairs skipped", "bogus,=empty,/state=/h", "/state/f", "/h/f"},
		{"trailing slash prefix", "/state/=/h", "/state/f", "/h/f"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HUB_DOCKER_HOST_ROOT", tc.root)
			if got := dockerBindSource(tc.path); got != tc.want {
				t.Fatalf("dockerBindSource(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
