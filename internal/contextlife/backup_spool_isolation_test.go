package contextlife

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupSharedSpoolExportsOnlyVerifiedOwner(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	os.MkdirAll(home, 0700)
	spool := filepath.Join(root, "spool")
	for _, dir := range []string{"schedules", "mappings", "outbox", "occurrences"} {
		os.MkdirAll(filepath.Join(spool, dir), 0700)
		for _, owner := range []string{"alice", "bob", ""} {
			label := owner
			if label == "" {
				label = "unknown"
			}
			body := `{"principal_id":"` + owner + `","context_id":"` + owner + `","text":"private-marker"}`
			if dir == "occurrences" {
				body = `{"job":` + body + `}`
			}
			os.WriteFile(filepath.Join(spool, dir, label+".json"), []byte(body), 0600)
		}
	}
	out := filepath.Join(root, "backup.zip")
	if err := Backup(home, "alice", out, spool); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	own := 0
	for _, f := range z.File {
		if !strings.HasPrefix(f.Name, "spool/") {
			continue
		}
		if strings.HasSuffix(f.Name, "/alice.json") {
			own++
		} else {
			t.Errorf("unverified spool record exported: %s", f.Name)
		}
	}
	if own != 4 {
		t.Errorf("own records missing: %d", own)
	}
}
