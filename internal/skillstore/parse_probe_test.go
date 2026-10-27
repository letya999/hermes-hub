package skillstore

import (
	"path/filepath"
	"testing"
)

func TestProbePlatformSkillParse(t *testing.T) {
	src := `C:\Users\User\a_projects\hermes-hub-m24\config\skills`
	idx, err := Build(filepath.Join(t.TempDir(), "view"), []Source{{Name: "platform", Dir: src}})
	t.Logf("skills=%v", idx)
	if err != nil {
		t.Fatal(err)
	}
}
