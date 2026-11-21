package agenttools

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveUnpackForgedDeclaredSize(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	// A real zip whose central directory lies: declared uncompressed size is
	// patched down so the declared-total check passes while actual bytes
	// exceed it.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 4096)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	idx := bytes.LastIndex(raw, []byte("PK\x01\x02"))
	if idx < 0 {
		t.Fatal("no central directory")
	}
	// uncompressed size lives at central-record offset 24.
	for i := 0; i < 4; i++ {
		raw[idx+24+i] = 0
	}
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "forged.zip"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(ctx, "archive_unpack", Input{Path: "forged.zip", Name: "forged"}); err == nil {
		t.Fatal("forged declared size unpacked")
	} else {
		t.Logf("rejected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", "forged")); !os.IsNotExist(err) {
		t.Fatal("forged unpack left files behind")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	if _, err := v.File("write", Input{Path: "notes/one.md", Text: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := v.File("write", Input{Path: "two.md", Text: "two"}); err != nil {
		t.Fatal(err)
	}
	res, err := v.Call(ctx, "archive_create", Input{Path: "artifacts/pack.zip", Paths: []string{"notes/one.md", "two.md"}})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := res.(map[string]any)
	if out["path"] != "artifacts/pack.zip" || out["files"] != 2 {
		t.Fatalf("create: %+v", out)
	}
	res, err = v.Call(ctx, "archive_unpack", Input{Path: "artifacts/pack.zip", Name: "restored"})
	if err != nil {
		t.Fatal(err)
	}
	up, _ := res.(map[string]any)
	if up["path"] != "artifacts/unpacked/restored" || up["files"] != 2 {
		t.Fatalf("unpack: %+v", up)
	}
	for name, want := range map[string]string{"notes/one.md": "one", "two.md": "two"} {
		b, err := os.ReadFile(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", "restored", filepath.FromSlash(name)))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	if _, err = v.Call(ctx, "archive_unpack", Input{Path: "artifacts/pack.zip", Name: "restored"}); err == nil {
		t.Fatal("second unpack over existing target")
	}
}

func TestArchiveCreateRejectsBadInputs(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	if _, err := v.File("write", Input{Path: "two.md", Text: "two"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []Input{
		{Path: "out.zip", Paths: []string{"two.md"}},
		{Path: "../evil.zip", Paths: []string{"two.md"}},
		{Path: "artifacts/x.zip", Paths: []string{}},
		{Path: "artifacts/x.zip", Paths: []string{"missing.md"}},
		{Path: "artifacts/x.zip", Paths: []string{"two.md", "two.md"}},
		{Path: "artifacts/x.zip", Paths: []string{"../secret"}},
		{Path: "artifacts/x.zip", Paths: []string{"artifacts"}},
		{Path: "artifacts/x.zip", Paths: []string{`a\b.md`}},
		{Path: "artifacts/x.tar", Paths: []string{"two.md"}},
	} {
		if _, err := v.Call(ctx, "archive_create", tc); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}

func TestArchiveUnpackFailsClosed(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	symlink := zip.FileHeader{Method: zip.Store}
	symlink.SetMode(os.ModeSymlink | 0o777)
	cases := map[string]map[string]any{
		"slip_parent.zip":    {"../evil.txt": "x"},
		"slip_abs.zip":       {"/etc/evil.txt": "x"},
		"slip_backslash.zip": {`a\b.txt`: "x"},
		"slip_drive.zip":     {"c:/evil.txt": "x"},
		"slip_dotdot.zip":    {"a/../../evil.txt": "x"},
		"slip_nul.zip":       {"ok.txt": "x", "bad\x00name": "y"},
		"encrypted.zip":      {"secret.txt": zip.FileHeader{Method: zip.Store, Flags: 0x1}},
		"symlink.zip":        {"link": symlink},
	}
	for zipName, entries := range cases {
		raw := makeZip(t, entries)
		if err := os.WriteFile(filepath.Join(v.Workspace.Name(), zipName), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := v.Call(ctx, "archive_unpack", Input{Path: zipName, Name: zipName + "-out"}); err == nil {
			t.Fatalf("unpack accepted %s", zipName)
		}
		if _, err := os.Stat(filepath.Join(v.Workspace.Name(), "evil.txt")); err == nil {
			t.Fatalf("%s escaped the workspace boundary", zipName)
		}
		if _, err := os.Stat(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", zipName+"-out")); !os.IsNotExist(err) {
			t.Fatalf("%s left a partial extraction behind", zipName)
		}
	}
}

func TestArchiveUnpackRejectsBadTarget(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	raw := makeZip(t, map[string]any{"ok.txt": "x"})
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "ok.zip"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Input{
		{Path: "ok.zip", Name: ""},
		{Path: "ok.zip", Name: "../evil"},
		{Path: "ok.zip", Name: "a/b"},
		{Path: "ok.zip", Name: `a\b`},
		{Path: "../ok.zip", Name: "x"},
		{Path: `a\b.zip`, Name: "x"},
	} {
		if _, err := v.Call(ctx, "archive_unpack", r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(v.Workspace.Name(), "artifacts", "ok.txt")); err == nil {
		t.Fatal("escaped artifacts/unpacked")
	}
}

func TestArchiveUnpackDirectories(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "d/", Method: zip.Store}
	h.SetMode(0o755 | os.ModeDir)
	if _, err := zw.CreateHeader(h); err != nil {
		t.Fatal(err)
	}
	w, err := zw.Create("d/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "dirs.zip"), buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(ctx, "archive_unpack", Input{Path: "dirs.zip", Name: "out"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", "out", "d", "f.txt"))
	if err != nil || string(b) != "x" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestArchiveUnpackLimitsAndCleanup(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	oldEntries, oldBytes := maxUnpackEntries, maxUnpackBytes
	maxUnpackEntries, maxUnpackBytes = 3, 16
	defer func() { maxUnpackEntries, maxUnpackBytes = oldEntries, oldBytes }()

	big := makeZip(t, map[string]any{"a": "x", "b": "x", "c": "x", "d": "x"})
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "big.zip"), big, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(ctx, "archive_unpack", Input{Path: "big.zip", Name: "big"}); err == nil {
		t.Fatal("accepted archive over entry limit")
	}
	if _, err := os.Stat(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", "big")); !os.IsNotExist(err) {
		t.Fatal("left a partial extraction behind")
	}

	fat := makeZip(t, map[string]any{"f": "0123456789abcdef0123456789"})
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "fat.zip"), fat, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(ctx, "archive_unpack", Input{Path: "fat.zip", Name: "fat"}); err == nil {
		t.Fatal("accepted archive over byte limit")
	}
	if _, err := os.Stat(filepath.Join(v.Workspace.Name(), "artifacts", "unpacked", "fat")); !os.IsNotExist(err) {
		t.Fatal("left a partial extraction behind")
	}

	// A lying central directory must not bypass the byte cap: the entry claims
	// 1 byte but the deflate stream yields more than maxUnpackBytes.
	liar := makeZip(t, map[string]any{"f": "0123456789abcdef0123456789"})
	i := bytes.Index(liar, []byte("PK\x01\x02"))
	if i < 0 {
		t.Fatal("no central directory")
	}
	for j := range 4 {
		liar[i+28+j] = 0
	}
	liar[i+28] = 1
	if err := os.WriteFile(filepath.Join(v.Workspace.Name(), "liar.zip"), liar, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Call(ctx, "archive_unpack", Input{Path: "liar.zip", Name: "liar"}); err == nil {
		t.Fatal("accepted an archive that lied about its size")
	}
}

func makeZip(t *testing.T, entries map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, spec := range entries {
		switch s := spec.(type) {
		case string:
			w, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(w, s); err != nil {
				t.Fatal(err)
			}
		case zip.FileHeader:
			h := s
			h.Name = name
			if _, err := zw.CreateHeader(&h); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
