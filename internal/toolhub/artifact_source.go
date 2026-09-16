package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- Git blob identifiers use SHA-1, not a credential/signature algorithm.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

var gitSHAPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var repositoryPartPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// ArtifactSource accepts public GitHub sources, never a ref or authenticated URL.
// Administrator-prepared sources use the same pinned archive/recipe contract.
type ArtifactSource struct {
	Repository string `json:"repository"`
	CommitSHA  string `json:"commit_sha"`
}

func (s ArtifactSource) ArchiveURL() (string, error) {
	u, err := url.Parse(s.Repository)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || !gitSHAPattern.MatchString(s.CommitSHA) {
		return "", fmt.Errorf("%w: public GitHub URL and exact lowercase 40-character commit SHA required", ErrInvalid)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) || !repositoryPartPattern.MatchString(parts[1]) || strings.Contains(parts[0]+parts[1], "..") || strings.HasSuffix(parts[1], ".git") {
		return "", fmt.Errorf("%w: canonical GitHub owner/repository URL required", ErrInvalid)
	}
	return "https://codeload.github.com/" + parts[0] + "/" + parts[1] + "/tar.gz/" + s.CommitSHA, nil
}

// FetchArtifactContext reads selected Git blobs from a verified public commit.
// It deliberately ignores ambient Git credentials, cookies and proxy settings.
// No archive is downloaded: unrelated repository file contents stay unread.
func FetchArtifactContext(ctx context.Context, source ArtifactSource, files []string, maxBytes int64) ([]byte, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchArtifactContext(ctx, client, source, files, maxBytes)
}

// FetchRepositoryArtifactContext automatically selects safe regular Git files.
// It does not download an archive containing excluded credentials. Only two
// GitHub API requests are needed; file reads use exact-SHA raw URLs and verified
// Git blob IDs, avoiding the anonymous API request quota per individual file.
func FetchRepositoryArtifactContext(ctx context.Context, source ArtifactSource, maxBytes int64) ([]byte, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return fetchArtifactContextMode(ctx, client, source, nil, maxBytes, true)
}

func fetchArtifactContext(ctx context.Context, client *http.Client, source ArtifactSource, files []string, maxBytes int64) ([]byte, error) {
	return fetchArtifactContextMode(ctx, client, source, files, maxBytes, false)
}

func fetchArtifactContextMode(ctx context.Context, client *http.Client, source ArtifactSource, files []string, maxBytes int64, discover bool) ([]byte, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return nil, err
	}
	if (!discover && len(files) == 0) || len(files) > 4096 || maxBytes < 1 || maxBytes > 64<<20 {
		return nil, fmt.Errorf("%w: bounded explicit context required", ErrInvalid)
	}
	wanted := map[string]bool{}
	for _, name := range files {
		if !artifactContextPath(name) || wanted[name] {
			return nil, fmt.Errorf("%w: unsafe or duplicate context path", ErrInvalid)
		}
		wanted[name] = true
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	base := "https://api.github.com/repos/" + strings.TrimPrefix(source.Repository, "https://github.com/") + "/git/"
	get := func(endpoint string, limit int64, value any) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("artifact source request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("artifact source request returned status %d", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil || int64(len(data)) > limit || json.Unmarshal(data, value) != nil {
			return fmt.Errorf("%w: malformed or oversized source response", ErrInvalid)
		}
		return nil
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := get("commits/"+source.CommitSHA, 1<<20, &commit); err != nil {
		return nil, err
	}
	if commit.SHA != source.CommitSHA || !gitSHAPattern.MatchString(commit.Tree.SHA) {
		return nil, fmt.Errorf("%w: source commit mismatch", ErrInvalid)
	}
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Tree      []struct {
			Path, Mode, Type, SHA string
			Size                  int64
		} `json:"tree"`
	}
	if err := get("trees/"+commit.Tree.SHA+"?recursive=1", 4<<20, &tree); err != nil {
		return nil, err
	}
	if tree.SHA != commit.Tree.SHA || tree.Truncated {
		return nil, fmt.Errorf("%w: incomplete source tree", ErrInvalid)
	}
	if discover {
		for _, entry := range tree.Tree {
			if entry.Type == "tree" || !artifactContextPath(entry.Path) || !artifactAutoBuildPath(entry.Path) {
				continue
			}
			if wanted[entry.Path] || len(files) >= 4096 {
				return nil, fmt.Errorf("%w: duplicate or oversized source tree", ErrInvalid)
			}
			wanted[entry.Path] = true
			files = append(files, entry.Path)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%w: no safe source files", ErrInvalid)
		}
		slices.Sort(files)
	}
	entries := map[string]struct {
		sha  string
		size int64
	}{}
	remaining := maxBytes
	for _, entry := range tree.Tree {
		if !wanted[entry.Path] {
			continue
		}
		if _, duplicate := entries[entry.Path]; duplicate || entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || !gitSHAPattern.MatchString(entry.SHA) || entry.Size < 0 || entry.Size > remaining {
			return nil, fmt.Errorf("%w: unsafe or oversized source file", ErrInvalid)
		}
		remaining -= entry.Size
		entries[entry.Path] = struct {
			sha  string
			size int64
		}{entry.SHA, entry.Size}
	}
	if len(entries) != len(wanted) {
		return nil, fmt.Errorf("%w: declared context files missing", ErrInvalid)
	}
	fetch := func(name string) ([]byte, error) {
		entry := entries[name]
		var data []byte
		if discover {
			segments := strings.Split(name, "/")
			for i := range segments {
				segments[i] = url.PathEscape(segments[i])
			}
			endpoint := "https://raw.githubusercontent.com/" + strings.TrimPrefix(source.Repository, "https://github.com/") + "/" + source.CommitSHA + "/" + strings.Join(segments, "/")
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return nil, err
			}
			response, err := client.Do(request)
			if err != nil {
				return nil, fmt.Errorf("artifact source request failed")
			}
			data, err = io.ReadAll(io.LimitReader(response.Body, entry.size+1))
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || err != nil || int64(len(data)) != entry.size {
				return nil, fmt.Errorf("%w: source file status or size mismatch", ErrInvalid)
			}
		} else {
			var blob struct {
				SHA, Encoding, Content string
				Size                   int64
			}
			if err := get("blobs/"+entry.sha, entry.size*2+4096, &blob); err != nil {
				return nil, err
			}
			var err error
			data, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
			if err != nil || blob.Encoding != "base64" || blob.SHA != entry.sha || blob.Size != entry.size || int64(len(data)) != entry.size {
				return nil, fmt.Errorf("%w: source blob mismatch", ErrInvalid)
			}
		}
		h := sha1.New() // #nosec G401 -- verify the Git object ID; runtime artifacts use SHA-256.
		_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
		_, _ = h.Write(data)
		if hex.EncodeToString(h.Sum(nil)) != entry.sha {
			return nil, fmt.Errorf("%w: source blob digest mismatch", ErrInvalid)
		}
		return data, nil
	}
	var contextBytes bytes.Buffer
	w := tar.NewWriter(&contextBytes)
	workers := 1
	if discover {
		workers = 8
	}
	// Bounded batches preserve deterministic tar order and the total byte budget.
	for start := 0; start < len(files); start += workers {
		batch := files[start:min(start+workers, len(files))]
		contents := make([][]byte, len(batch))
		errors := make([]error, len(batch))
		var pending sync.WaitGroup
		for i, name := range batch {
			pending.Go(func() { contents[i], errors[i] = fetch(name) })
		}
		pending.Wait()
		for i, name := range batch {
			if errors[i] != nil {
				return nil, errors[i]
			}
			if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: entries[name].size, Typeflag: tar.TypeReg}); err != nil {
				return nil, err
			}
			if _, err := w.Write(contents[i]); err != nil {
				return nil, err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return contextBytes.Bytes(), nil
}

func artifactContextPath(name string) bool {
	if name == "" || name == "." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00\r\n") || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		if part == ".git" || part == ".ssh" || part == "spaces" || part == "node_modules" || part == ".venv" || part == ".npmrc" || part == ".netrc" || part == ".pypirc" || part == ".mcp.json" || part == "claude_desktop_config.json" || part == ".env" || strings.HasPrefix(part, ".env.") || strings.Contains(part, "credential") || strings.Contains(part, "secret") || strings.HasPrefix(part, "id_rsa") || strings.HasPrefix(part, "id_ed25519") || strings.HasSuffix(part, ".pem") || strings.HasSuffix(part, ".key") || strings.HasSuffix(part, ".p12") || strings.HasSuffix(part, ".pfx") || part == "token.json" || part == "tokens.json" {
			return false
		}
	}
	return true
}

// artifactAutoBuildPath keeps generated language contexts focused on files
// consumed by the package build. Repository documentation is not needed to
// compile an MCP and often contains PEM examples that must not block an
// otherwise safe build. Explicitly requested files still use the stricter
// artifactContextPath gate above.
func artifactAutoBuildPath(name string) bool {
	parts := strings.Split(strings.ToLower(name), "/")
	return len(parts) == 0 || (parts[0] != "docs" && parts[0] != "documentation")
}

// CopyArtifactContext exports only explicitly reviewed regular files. It never
// extracts upstream paths onto the host and rejects links/devices even when a
// recipe asks for them. Caller supplies a decompressed, bounded source archive.
// Filename filtering is defense in depth, not a substitute for secret scanning.
func CopyArtifactContext(dst io.Writer, src io.Reader, prefix string, files []string, maxBytes int64) error {
	if prefix == "" || strings.ContainsAny(prefix, "/\\") || len(files) == 0 || len(files) > 4096 || maxBytes < 1 || maxBytes > 64<<20 {
		return fmt.Errorf("%w: bounded explicit context required", ErrInvalid)
	}
	wanted := map[string]bool{}
	for _, name := range files {
		if !artifactContextPath(name) || wanted[name] {
			return fmt.Errorf("%w: unsafe or duplicate context path", ErrInvalid)
		}
		wanted[name] = true
	}
	r := tar.NewReader(io.LimitReader(src, 128<<20))
	w := tar.NewWriter(dst)
	remaining := maxBytes
	seen := map[string]bool{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: malformed source archive", ErrInvalid)
		}
		name, ok := strings.CutPrefix(h.Name, prefix+"/")
		if !ok || !wanted[name] {
			continue
		}
		if seen[name] || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > remaining {
			return fmt.Errorf("%w: duplicate, non-regular or oversized context file", ErrInvalid)
		}
		seen[name] = true
		remaining -= h.Size
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: h.Size, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := io.CopyN(w, r, h.Size); err != nil {
			return err
		}
	}
	if len(seen) != len(wanted) {
		return fmt.Errorf("%w: declared context files missing", ErrInvalid)
	}
	return w.Close()
}
