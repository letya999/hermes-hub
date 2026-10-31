package skillstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// Exec flags are hub state, not filesystem permission bits — they do not
// survive on every host. The live exec list lives at
// .state/exec/<name>.json; retained revisions keep a <rev>.exec sidecar.

func (s *Store) execPath(name string) string {
	return filepath.Join(s.root, stateDir, "exec", name+".json")
}

func execPaths(tree Tree) []string {
	paths := []string{}
	for path, f := range tree {
		if f.Exec {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

func (s *Store) writeExec(name string, paths []string) error {
	body, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	return writeJSONFile(s.execPath(name), append(body, '\n'))
}

func (s *Store) readExec(name string) []string {
	return readExecList(s.execPath(name))
}

func readExecList(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var paths []string
	if json.Unmarshal(body, &paths) != nil {
		return nil
	}
	return paths
}

func writeJSONFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
