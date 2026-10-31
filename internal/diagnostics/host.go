package diagnostics

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// HostLog mirrors supervisor log lines to a bounded local file so the
// container collector can include host-side lifecycle activity.
func HostLog(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0770); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe diagnostic directory")
	}
	if err := os.Chmod(filepath.Dir(path), 0770); err != nil {
		return nil, err
	}
	previous := log.Writer()
	w := &boundedFile{path: path}
	log.SetOutput(io.MultiWriter(previous, w))
	return func() { log.SetOutput(previous) }, nil
}

type boundedFile struct {
	mu   sync.Mutex
	path string
}

func (w *boundedFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := len(p)
	if len(p) > maxDiagnosticLine {
		p = []byte("[diagnostic-gap] reason=oversized_host_record\n")
	}
	if err := appendDiagnostic(w.path, p, 10<<20); err != nil {
		return 0, err
	}
	return count, nil
}
