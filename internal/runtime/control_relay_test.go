package runtime

import (
	"strings"
	"testing"
)

func TestControlRelayVsockTargetValidation(t *testing.T) {
	cases := []struct {
		target  string
		wantErr string
	}{
		{"vsock://8080/work/box-1/v.sock", ""},
		{"vsock://65535/a/b/c.sock", ""},
		{"vsock://abc/work/v.sock", "vsock://"},
		{"vsock://0/work/v.sock", "vsock://"},
		{"vsock://70000/work/v.sock", "vsock://"},
		{"vsock://8080/relative-nosuffix", "vsock://"},
		{"vsock://8080/../etc/escape.sock", "vsock://"},
		{"vsock://8080/work/v.sock?x=1", "fixed HTTP origin"},
		{"vsock://user@8080/work/v.sock", "fixed HTTP origin"},
		{"vsock://8080", "vsock://"},
		{"http://runtime:8080", ""},
		{"http://", "fixed HTTP origin"},
		{"ftp://host/x", "fixed HTTP origin"},
	}
	for _, tc := range cases {
		_, err := controlRelayHandler(tc.target)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.target, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: expected %q error, got %v", tc.target, tc.wantErr, err)
		}
	}
}

func TestControlRelayDeniedBodyKeysUnchanged(t *testing.T) {
	for _, key := range []string{"hosted_room_dispatch", "_room_execution_policy"} {
		found := false
		for _, denied := range controlDeniedBodyKeys {
			if denied == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("denied key %s missing", key)
		}
	}
}
