package seal

import (
	"bytes"
	"testing"
)

func TestAEAD(t *testing.T) {
	if _, e := New([]byte("bad")); e == nil {
		t.Fatal("short key")
	}
	key := Derive(bytes.Repeat([]byte{7}, 32), "test")
	b, e := New(key)
	if e != nil {
		t.Fatal(e)
	}
	plain := []byte("synthetic-secret-for-test")
	aad := []byte("owned/by/alice")
	x, e := b.Seal(plain, aad)
	if e != nil {
		t.Fatal(e)
	}
	y, e := b.Seal(plain, aad)
	if e != nil || bytes.Equal(x, y) {
		t.Fatal("nonce reuse")
	}
	got, e := b.Open(x, aad)
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatal("roundtrip")
	}
	for _, bad := range [][]byte{x[:4], append([]byte(nil), x...)} {
		if len(bad) > 4 {
			bad[len(bad)-1] ^= 1
		}
		if _, e := b.Open(bad, aad); e == nil {
			t.Fatal("tamper accepted")
		}
	}
	if _, e := b.Open(x, []byte("owned/by/bob")); e == nil {
		t.Fatal("AAD substitution")
	}
	if bytes.Equal(Derive(key, "one"), Derive(key, "two")) {
		t.Fatal("domain separation")
	}
	if len(Random()) != 43 || Random() == Random() {
		t.Fatal("random ID")
	}
	if !Equal(Digest("x"), Digest("x")) || Equal(Digest("x"), Digest("y")) {
		t.Fatal("digest")
	}
	Wipe(got)
	if !bytes.Equal(got, make([]byte, len(got))) {
		t.Fatal("wipe")
	}
}
