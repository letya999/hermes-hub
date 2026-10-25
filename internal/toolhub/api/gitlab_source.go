package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- Git object verification.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

func fetchGitLabContext(ctx context.Context, client *http.Client, source ArtifactSource, files []string, maxBytes int64, discover, recipeMetadata, full bool) ([]byte, error) {
	if _, err := source.ArchiveURL(); err != nil {
		return nil, err
	}
	if (!discover && len(files) == 0) || len(files) > 4096 || maxBytes < 1 || maxBytes > 64<<20 {
		return nil, ErrInvalid
	}
	wanted := map[string]bool{}
	for _, name := range files {
		if !ArtifactContextPath(name) && !(recipeMetadata && recipeContextPath(name)) || wanted[name] {
			return nil, ErrInvalid
		}
		wanted[name] = true
	}
	base := "https://gitlab.com/api/v4/projects/" + url.PathEscape(strings.TrimPrefix(source.Repository, "https://gitlab.com/")) + "/repository/"
	get := func(endpoint string, limit int64, value any) (http.Header, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+endpoint, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("GitLab source request failed")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitLab source returned status %d", response.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil || int64(len(data)) > limit || json.Unmarshal(data, value) != nil {
			return nil, ErrInvalid
		}
		return response.Header, nil
	}
	var commit struct {
		ID string `json:"id"`
	}
	if _, err := get("commits/"+source.CommitSHA, 1<<20, &commit); err != nil {
		return nil, err
	}
	if commit.ID != source.CommitSHA {
		return nil, ErrInvalid
	}
	type entry struct{ Path, Mode, Type, ID string }
	entries := map[string]entry{}
	outputNames := map[string]string{}
	for page := 1; ; page++ {
		var batch []entry
		headers, err := get(fmt.Sprintf("tree?ref=%s&recursive=true&per_page=100&page=%d", source.CommitSHA, page), 4<<20, &batch)
		if err != nil {
			return nil, err
		}
		for _, item := range batch {
			name := item.Path
			if discover {
				if source.Subfolder != "" {
					var ok bool
					name, ok = strings.CutPrefix(name, source.Subfolder+"/")
					if !ok {
						continue
					}
					if recipeMetadata {
						name = item.Path
					}
				}
				if item.Type != "blob" || item.Mode != "100644" && item.Mode != "100755" || (!ArtifactContextPath(name) && !(recipeMetadata && recipeContextPath(name))) || (!recipeMetadata && !full && !artifactAutoBuildPath(name)) || (!recipeMetadata && artifactBinaryAssetPath(name)) {
					continue
				}
				if wanted[item.Path] || len(files) >= 4096 {
					return nil, ErrInvalid
				}
				wanted[item.Path] = true
				files = append(files, item.Path)
				outputNames[item.Path] = name
			}
			if !wanted[item.Path] {
				continue
			}
			if _, ok := entries[item.Path]; ok || item.Type != "blob" || item.Mode != "100644" && item.Mode != "100755" || !gitSHAPattern.MatchString(item.ID) {
				return nil, ErrInvalid
			}
			entries[item.Path] = item
		}
		if headers.Get("X-Next-Page") == "" {
			break
		}
		if page >= 41 {
			return nil, ErrInvalid
		}
	}
	if len(files) == 0 || len(entries) != len(wanted) {
		return nil, ErrInvalid
	}
	slices.Sort(files)
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	remaining := maxBytes
	for _, name := range files {
		item := entries[name]
		var blob struct {
			SHA, Encoding, Content string
			Size                   int64
		}
		if _, err := get("blobs/"+item.ID, remaining*2+4096, &blob); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
		if err != nil || blob.Encoding != "base64" || blob.SHA != item.ID || blob.Size != int64(len(data)) || blob.Size > remaining {
			return nil, ErrInvalid
		}
		h := sha1.New() // #nosec G401 -- verify Git object identity.
		_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
		_, _ = h.Write(data)
		if hex.EncodeToString(h.Sum(nil)) != item.ID {
			return nil, ErrInvalid
		}
		remaining -= blob.Size
		outputName := name
		if value := outputNames[name]; value != "" {
			outputName = value
		}
		if err := writer.WriteHeader(&tar.Header{Name: outputName, Mode: 0644, Size: blob.Size, Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func parseGitLabSource(raw string, pinned bool) (ArtifactSource, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host != "gitlab.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return ArtifactSource{}, ErrInvalid
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	source := ArtifactSource{}
	for i, part := range parts {
		if part != "-" {
			continue
		}
		if !pinned || i+2 >= len(parts) || parts[i+1] != "tree" || !gitSHAPattern.MatchString(parts[i+2]) {
			return source, ErrInvalid
		}
		source.CommitSHA = parts[i+2]
		source.Subfolder = strings.Join(parts[i+3:], "/")
		parts = parts[:i]
		break
	}
	if len(parts) < 2 || pinned && source.CommitSHA == "" || !validGitHubSubfolder(source.Subfolder) {
		return source, ErrInvalid
	}
	for _, part := range parts {
		if !repositoryPartPattern.MatchString(part) || strings.Contains(part, "..") {
			return ArtifactSource{}, ErrInvalid
		}
	}
	source.Repository = "https://gitlab.com/" + strings.Join(parts, "/")
	return source, nil
}

func resolveGitLabSource(ctx context.Context, raw string, client *http.Client) (ArtifactSource, error) {
	source, err := parseGitLabSource(raw, false)
	if err != nil {
		return ArtifactSource{}, err
	}
	endpoint := "https://gitlab.com/api/v4/projects/" + url.PathEscape(strings.TrimPrefix(source.Repository, "https://gitlab.com/")) + "/repository/commits/HEAD"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ArtifactSource{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ArtifactSource{}, fmt.Errorf("GitLab source resolution failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ArtifactSource{}, fmt.Errorf("GitLab source resolution returned status %d", response.StatusCode)
	}
	var commit struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&commit); err != nil || !gitSHAPattern.MatchString(commit.ID) {
		return ArtifactSource{}, fmt.Errorf("%w: GitLab commit", ErrInvalid)
	}
	source.CommitSHA = commit.ID
	return source, nil
}
