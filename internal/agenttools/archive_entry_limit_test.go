package agenttools

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestArchiveSingleEntryLimit(t *testing.T) {
	for _, size := range []int{8 << 20, (8 << 20) + 1} {
		t.Run(string(rune('a'+size%2)), func(t *testing.T) {
			var packed bytes.Buffer
			writer := zip.NewWriter(&packed)
			entry, err := writer.Create("one.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(strings.Repeat("x", size))); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			reader, err := zip.NewReader(bytes.NewReader(packed.Bytes()), int64(packed.Len()))
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			_, err = unpackZip(root, reader, "out")
			if size == 8<<20 {
				if err != nil {
					t.Fatalf("entry at limit rejected: %v", err)
				}
				info, err := root.Stat("out/one.txt")
				if err != nil || info.Size() != int64(size) {
					t.Fatalf("exact-limit output: %v %v", info, err)
				}
			} else {
				if err == nil {
					t.Fatal("entry above artifact bound accepted despite being under aggregate bound")
				}
				if _, err := root.Stat("out"); !os.IsNotExist(err) {
					t.Fatalf("partial target left: %v", err)
				}
			}
		})
	}
}
