package runtime

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ArtifactRef is one generated deliverable under workspace/artifacts. Path is
// relative to the artifacts root ("documents/report.pdf"). Blob and Error are
// populated only on the gateway side after the bytes crossed this contract.
type ArtifactRef struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Mime  string `json:"mime,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Blob  string `json:"blob,omitempty"`
	Error string `json:"error,omitempty"`
}

// ArtifactRequest fetches one artifact from the runtime owning the envelope.
// Resident runtimes ignore the envelope; the supervisor uses it to route.
type ArtifactRequest struct {
	ExecuteRequest
	Name string `json:"name"`
}

const (
	artifactMaxCount = 8
	artifactMaxBytes = 8 << 20
	artifactScanSkew = 2 * time.Second
)

var artifactMimes = map[string]string{
	".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv",
	".html": "text/html", ".pdf": "application/pdf",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".png":  "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
}

func artifactRoot() string { return filepath.Join(workspace, "artifacts") }

// scanArtifacts lists files the run produced under artifacts/documents and
// artifacts/images. The run-start timestamp bounds attribution; a zero time
// (recovered observation) lists nothing rather than guessing.
func scanArtifacts(since time.Time) []ArtifactRef {
	if since.IsZero() {
		return nil
	}
	root := artifactRoot()
	var found []ArtifactRef
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || len(found) >= artifactMaxCount {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !validArtifactRel(rel) {
			return nil
		}
		info, err := entry.Info()
		if err != nil || entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() == 0 || info.Size() > artifactMaxBytes || info.ModTime().Before(since.Add(-artifactScanSkew)) {
			return nil
		}
		mime := artifactMimes[strings.ToLower(filepath.Ext(path))]
		if mime == "" {
			mime = "application/octet-stream"
		}
		found = append(found, ArtifactRef{Name: filepath.Base(path), Path: filepath.ToSlash(rel), Mime: mime, Size: info.Size()})
		return nil
	})
	slices.SortFunc(found, func(a, b ArtifactRef) int { return strings.Compare(a.Path, b.Path) })
	return found
}

func validArtifactRel(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 2 && (parts[0] == "documents" || parts[0] == "images") && parts[1] != "" && parts[1] == filepath.Base(parts[1]) && parts[1] != "." && parts[1] != ".."
}

// artifact resolves one bounded file under the artifacts root. Path traversal,
// symlinks, oversized and empty files are refused.
func (s *runtimeHTTP) artifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var request ArtifactRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeRuntimeError(w, http.StatusBadRequest, "invalid artifact request")
		return
	}
	if !validArtifactRel(request.Name) {
		writeRuntimeError(w, http.StatusBadRequest, "invalid artifact name")
		return
	}
	root := artifactRoot()
	path := filepath.Join(root, filepath.FromSlash(request.Name))
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		writeRuntimeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	if info.Size() == 0 || info.Size() > artifactMaxBytes {
		writeRuntimeError(w, http.StatusRequestEntityTooLarge, "artifact size outside delivery bounds")
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		writeRuntimeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != filepath.Join(resolvedRoot, filepath.FromSlash(request.Name)) {
		writeRuntimeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		writeRuntimeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	defer file.Close()
	if mime := artifactMimes[strings.ToLower(filepath.Ext(path))]; mime != "" {
		w.Header().Set("Content-Type", mime)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, io.LimitReader(file, artifactMaxBytes+1))
}
