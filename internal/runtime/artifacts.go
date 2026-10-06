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
	// Videos ride the same contract but need more room: 48 MiB keeps them
	// under Telegram's 50 MB bot upload bound.
	videoMaxBytes    = 48 << 20
	artifactScanSkew = 2 * time.Second
)

// artifactLimit is the per-bucket byte cap: videos get headroom, documents
// and images stay at the original bound.
func artifactLimit(rel string) int64 {
	if strings.HasPrefix(filepath.ToSlash(rel), "videos/") {
		return videoMaxBytes
	}
	return artifactMaxBytes
}

var artifactMimes = map[string]string{
	".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv",
	".html": "text/html", ".pdf": "application/pdf",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".png":  "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".webp": "image/webp",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
}

func artifactRoot() string { return filepath.Join(workspace, "artifacts") }

// runStartsDir keeps one zero-byte marker per admitted run; the file's mtime
// is the run's start bound. observeHermesRun recovers it after an interruption
// so scanArtifacts still attributes files the original run produced.
func runStartsDir() string { return filepath.Join(state, "runstarts") }

func markRunStart(runID string) {
	if !validHermesRunID(runID) {
		return
	}
	// /state exists only in a deployed runtime; refusing to create it keeps
	// test runs from sprouting a stray directory on the host filesystem.
	if info, err := os.Stat(state); err != nil || !info.IsDir() {
		return
	}
	dir := runStartsDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, runID), nil, 0600)
	// Sweep markers older than a day — runs never outlive that, and the
	// directory would otherwise accumulate one file per admission.
	cutoff := time.Now().Add(-24 * time.Hour)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// runStartFor returns the recorded admission bound for a recovered run, or the
// zero time when no marker exists — scanArtifacts then lists nothing, exactly
// like before the marker contract existed.
func runStartFor(runID string) time.Time {
	if !validHermesRunID(runID) {
		return time.Time{}
	}
	info, err := os.Stat(filepath.Join(runStartsDir(), runID))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// scanArtifacts lists files the run produced under artifacts/documents,
// artifacts/images and artifacts/videos. The run-start timestamp bounds
// attribution; a zero time (recovered observation) lists nothing.
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
		if info.Size() == 0 || info.Size() > artifactLimit(rel) || info.ModTime().Before(since.Add(-artifactScanSkew)) {
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

// extractMediaArtifacts lifts upstream "MEDIA:<path>" marker lines out of the
// reply text. Each file resolving inside the workspace is staged under
// artifacts/documents|images|videos by mime (copied there when the tool wrote it elsewhere,
// e.g. the workspace root) and returned as a ref; a marker pointing at an
// unusable file still yields a ref with Error so delivery stays explicit.
// Marker lines are always stripped — they are routing metadata, not prose.
func extractMediaArtifacts(text string) (string, []ArtifactRef) {
	var kept []string
	var refs []ArtifactRef
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "MEDIA:") {
			kept = append(kept, line)
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "MEDIA:"))
		if seen[raw] {
			continue
		}
		seen[raw] = true
		ref, ok := stageMediaArtifact(raw)
		if !ok {
			name := filepath.Base(filepath.FromSlash(raw))
			if name == "." || name == string(filepath.Separator) || name == "" {
				continue
			}
			refs = append(refs, ArtifactRef{Name: name, Error: "media file unavailable"})
			continue
		}
		refs = append(refs, ref)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), refs
}

// extractVoiceLines lifts upstream "VOICE:<text>" marker lines out of the
// reply text: the joined payload is what the channel synthesizes and sends
// as a voice message. Marker lines are routing metadata and never reach the
// visible message; when the stripped text is empty the reply is voice-only.
func extractVoiceLines(text string) (string, string) {
	var kept, voiced []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "VOICE:") {
			kept = append(kept, line)
			continue
		}
		if spoken := strings.TrimSpace(strings.TrimPrefix(trimmed, "VOICE:")); spoken != "" {
			voiced = append(voiced, spoken)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), strings.Join(voiced, "\n")
}

// stageMediaArtifact resolves a workspace path and copies the file into the
// matching artifacts bucket when it does not already live under
// artifacts/{documents,images,videos}. Containment, regular-file and size
// checks are the same contract the artifact endpoint enforces.
func stageMediaArtifact(raw string) (ArtifactRef, bool) {
	if raw == "" || len(raw) > 1024 || strings.ContainsRune(raw, 0) {
		return ArtifactRef{}, false
	}
	root := workspace
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, filepath.FromSlash(candidate))
	}
	if !filepath.IsAbs(raw) && filepath.Base(filepath.FromSlash(raw)) == filepath.FromSlash(raw) {
		// Models often emit a bare filename for a file they just wrote under
		// artifacts/. Resolve those before treating the marker as missing.
		for _, bucket := range []string{"images", "documents", "videos"} {
			if alt := filepath.Join(root, "artifacts", bucket, filepath.FromSlash(raw)); fileExists(alt) {
				candidate = alt
				break
			}
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ArtifactRef{}, false
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
		// Models routinely quote artifact paths under the agent home
		// ($HOME/artifacts/...) although the tools tree is the workspace; remap
		// a failed absolute candidate onto the artifacts root before giving up.
		if filepath.IsAbs(raw) {
			if i := strings.Index(filepath.ToSlash(candidate), "/artifacts/"); i >= 0 {
				tail := filepath.ToSlash(candidate)[i+len("/artifacts/"):]
				if alt, altErr := filepath.EvalSymlinks(filepath.Join(artifactRoot(), filepath.FromSlash(tail))); altErr == nil &&
					strings.HasPrefix(alt, resolvedRoot+string(os.PathSeparator)) {
					resolved, err = alt, nil
				}
			}
		}
	}
	if err != nil || resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
		return ArtifactRef{}, false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return ArtifactRef{}, false
	}
	mime := artifactMimes[strings.ToLower(filepath.Ext(resolved))]
	if mime == "" {
		mime = "application/octet-stream"
	}
	bucket := "documents"
	if strings.HasPrefix(mime, "image/") {
		bucket = "images"
	} else if strings.HasPrefix(mime, "video/") {
		bucket = "videos"
	}
	if info.Size() > artifactLimit(bucket+"/x") {
		return ArtifactRef{}, false
	}
	if artRoot, err := filepath.EvalSymlinks(artifactRoot()); err == nil {
		if rel, err := filepath.Rel(artRoot, resolved); err == nil && validArtifactRel(rel) {
			return ArtifactRef{Name: filepath.Base(resolved), Path: filepath.ToSlash(rel), Mime: mime, Size: info.Size()}, true
		}
	}
	dest := filepath.Join(artifactRoot(), bucket, filepath.Base(resolved))
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return ArtifactRef{}, false
	}
	if err := copyBounded(resolved, dest, info.Size()); err != nil {
		return ArtifactRef{}, false
	}
	return ArtifactRef{Name: filepath.Base(dest), Path: bucket + "/" + filepath.Base(dest), Mime: mime, Size: info.Size()}, true
}

func copyBounded(src, dst string, size int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(out, in, size); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// mergeArtifacts combines window-scanned and MEDIA-referenced files, keeping
// the first ref for each artifact path. A failed marker ref whose name a
// delivered ref already carries is dropped: reporting "delivery failed" for a
// file that did arrive is noise, not information.
func mergeArtifacts(groups ...[]ArtifactRef) []ArtifactRef {
	seen := map[string]bool{}
	delivered := map[string]bool{}
	for _, group := range groups {
		for _, ref := range group {
			if ref.Path != "" {
				delivered[ref.Name] = true
			}
		}
	}
	var out []ArtifactRef
	for _, group := range groups {
		for _, ref := range group {
			key := ref.Path
			if key == "" {
				key = "error:" + ref.Name + ":" + ref.Error
			}
			if seen[key] || len(out) >= artifactMaxCount {
				continue
			}
			if ref.Path == "" && delivered[ref.Name] {
				continue
			}
			seen[key] = true
			out = append(out, ref)
		}
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func validArtifactRel(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 2 && (parts[0] == "documents" || parts[0] == "images" || parts[0] == "videos") && parts[1] != "" && parts[1] == filepath.Base(parts[1]) && parts[1] != "." && parts[1] != ".."
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
	if info.Size() == 0 || info.Size() > artifactLimit(request.Name) {
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
	_, _ = io.Copy(w, io.LimitReader(file, artifactLimit(request.Name)+1))
}
