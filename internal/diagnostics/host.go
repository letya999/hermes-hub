package diagnostics

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// HostLog mirrors supervisor log lines to a bounded local file so the
// container collector can include host-side lifecycle activity.
func HostLog(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
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
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if info, err := os.Stat(w.path); err == nil && info.Size()+int64(len(p)) > 10<<20 {
		flags = os.O_CREATE | os.O_TRUNC | os.O_WRONLY
	}
	f, err := os.OpenFile(w.path, flags, 0600) // #nosec G304 -- caller provides fixed project .local path.
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Write(p)
}
