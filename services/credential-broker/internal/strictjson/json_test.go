package strictjson

import (
	"strings"
	"testing"
)

func TestStrictBoundaries(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
	}{
		{`{"name":"ok","nested":{"x":1},"a":[1,2,null,true]}`, true},
		{`{"x":1,"x":2}`, false}, {`{"a":{"x":1,"x":2}}`, false}, {`{"x":1} {"x":2}`, false},
		{`[1,]`, false}, {`{"x":}`, false}, {``, false}, {`null`, true}, {`"x"`, true}, {`}`, false},
		{strings.Repeat("[", 35) + "0" + strings.Repeat("]", 35), false}, {string([]byte{0xff}), false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := Valid([]byte(tc.raw)); got != tc.ok {
				t.Fatalf("got %v", got)
			}
		})
	}
	var dst struct {
		Name string `json:"name"`
	}
	if Decode([]byte(`{"name":"ok"}`), &dst) != nil || dst.Name != "ok" {
		t.Fatal("valid decode")
	}
	for _, raw := range []string{`{"unknown":1}`, `{"name":1}`, `{"name":"a","name":"b"}`, `{} []`, string([]byte{0xff})} {
		if Decode([]byte(raw), &dst) == nil {
			t.Fatal("accepted", raw)
		}
	}
}
func FuzzDecode(f *testing.F) {
	for _, s := range []string{`{}`, `{"a":1,"a":2}`, `[]`, `null`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 65536 {
			return
		}
		var out map[string]any
		if e := Decode(b, &out); e == nil && !Valid(b) {
			t.Fatal("decoder accepted invalid input")
		}
	})
}
