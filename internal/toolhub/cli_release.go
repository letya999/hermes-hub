package toolhub

// github-release: install source. A release is mutable (tags move, assets get
// re-uploaded), so the pin is the verified asset sha256 recorded on the
// definition plus the resulting image manifest digest. The verified binary is
// wrapped in a FROM-scratch image through the same restricted-build and
// evidence pipeline a source build takes.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

var releaseTagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// parseGitHubReleaseSource parses "github-release:owner/repo@tag" plus the
// spec fields "asset" (exact name or *-wildcard), optional "digest"
// (sha256 pin) and optional "member" (archive member to extract).
func parseGitHubReleaseSource(raw, asset, digest, member string) (ArtifactSource, error) {
	rest := strings.TrimPrefix(raw, "github-release:")
	repo, tag, ok := strings.Cut(rest, "@")
	if !ok || repo == "" || tag == "" || strings.Contains(rest, "..") {
		return ArtifactSource{}, fmt.Errorf("%w: github-release source must be github-release:owner/repo@tag", ErrInvalid)
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !repositoryPartPattern.MatchString(parts[0]) || !repositoryPartPattern.MatchString(parts[1]) {
		return ArtifactSource{}, fmt.Errorf("%w: github-release repository must be owner/repo", ErrInvalid)
	}
	if !releaseTagPattern.MatchString(tag) || strings.HasPrefix(tag, "/") || strings.HasSuffix(tag, "/") || strings.Contains(tag, "//") {
		return ArtifactSource{}, fmt.Errorf("%w: invalid release tag", ErrInvalid)
	}
	if asset == "" || len(asset) > 256 || strings.ContainsAny(asset, "/\\\x00\r\n") || strings.Contains(asset, "..") {
		return ArtifactSource{}, fmt.Errorf("%w: github-release requires an asset name pattern", ErrInvalid)
	}
	if member != "" && (len(member) > 256 || strings.ContainsAny(member, "\\\x00\r\n") || strings.Contains(member, "..") || strings.HasPrefix(member, "/")) {
		return ArtifactSource{}, fmt.Errorf("%w: invalid release archive member", ErrInvalid)
	}
	if digest != "" && !digestPattern.MatchString(normalizeDigestPrefix(digest)) {
		return ArtifactSource{}, fmt.Errorf("%w: release digest must be sha256:<64-hex>", ErrInvalid)
	}
	return ArtifactSource{
		Repository:  "https://github.com/" + parts[0] + "/" + parts[1],
		Tag:         tag,
		Asset:       asset,
		AssetDigest: normalizeDigestPrefix(digest),
		AssetMember: member,
	}, nil
}

func normalizeDigestPrefix(digest string) string {
	if digest == "" || strings.HasPrefix(digest, "sha256:") {
		return digest
	}
	return "sha256:" + digest
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
}

type githubReleaseResponse struct {
	TagName string               `json:"tag_name"`
	Assets  []githubReleaseAsset `json:"assets"`
}

const cliReleaseMaxAssetBytes = 128 << 20

func releaseHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: nil},
		// Release downloads 302 to *.githubusercontent.com; only https hops
		// are followed and the payload digest is verified regardless.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			host := req.URL.Hostname()
			if host != "github.com" && !strings.HasSuffix(host, ".github.com") && !strings.HasSuffix(host, ".githubusercontent.com") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func githubAPIBase() string {
	return "https://api.github.com"
}

func fetchReleaseJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: release metadata request", ErrInvalid)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hermes-hub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("release metadata request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("release metadata status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(target)
}

func fetchReleaseBytes(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: release download request", ErrInvalid)
	}
	req.Header.Set("User-Agent", "hermes-hub/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("release download failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit+1))
}

// fetchReleaseBinary resolves, verifies and extracts the release binary. It
// returns the binary bytes, the resolved asset name, the verified sha256
// digest and the checksum record kept in the recipe context. defaultMember
// selects the archive member when the spec declares none — the tool's own
// basename.
func fetchReleaseBinary(ctx context.Context, source ArtifactSource, client *http.Client, apiBase, defaultMember string) (binary []byte, assetName, assetDigest, resolvedTag string, checksums []byte, err error) {
	parts := strings.Split(strings.TrimPrefix(source.Repository, "https://github.com/"), "/")
	if len(parts) != 2 || source.Tag == "" {
		return nil, "", "", "", nil, fmt.Errorf("%w: github-release source must be owner/repo@tag", ErrInvalid)
	}
	var release githubReleaseResponse
	if source.Tag == "latest" {
		// latest resolves through the API and is then pinned to the resolved
		// tag; the recorded tag keeps the review record honest.
		if err := fetchReleaseJSON(ctx, client, apiBase+"/repos/"+parts[0]+"/"+parts[1]+"/releases/latest", &release); err != nil {
			return nil, "", "", "", nil, err
		}
	} else {
		if err := fetchReleaseJSON(ctx, client, apiBase+"/repos/"+parts[0]+"/"+parts[1]+"/releases/tags/"+source.Tag, &release); err != nil {
			return nil, "", "", "", nil, err
		}
		if release.TagName != source.Tag {
			return nil, "", "", "", nil, fmt.Errorf("%w: release tag mismatch", ErrStale)
		}
	}
	var asset *githubReleaseAsset
	var checksumAsset *githubReleaseAsset
	for i := range release.Assets {
		a := &release.Assets[i]
		if isChecksumAsset(a.Name) {
			checksumAsset = a
			continue
		}
		matched, err := path.Match(source.Asset, a.Name)
		if err != nil {
			return nil, "", "", "", nil, fmt.Errorf("%w: invalid asset pattern", ErrInvalid)
		}
		if matched {
			if asset != nil {
				return nil, "", "", "", nil, fmt.Errorf("%w: asset pattern matches multiple release assets: %s, %s (narrow the asset glob)", ErrStale, asset.Name, a.Name)
			}
			asset = a
		}
	}
	if asset == nil {
		names := make([]string, 0, len(release.Assets))
		for _, a := range release.Assets {
			names = append(names, a.Name)
		}
		return nil, "", "", "", nil, fmt.Errorf("%w: release asset not found; available: %s", ErrNotFound, strings.Join(names, ", "))
	}

	// Collect every available digest source; all present must agree.
	expected := map[string]bool{}
	if source.AssetDigest != "" {
		expected[source.AssetDigest] = true
	}
	if asset.Digest != "" {
		expected[asset.Digest] = true
	}
	var checksumRecord []byte
	if checksumAsset != nil {
		data, err := fetchReleaseBytes(ctx, client, checksumAsset.BrowserDownloadURL, 4<<20)
		if err != nil {
			return nil, "", "", "", nil, err
		}
		if digest := checksumForAsset(data, asset.Name); digest != "" {
			expected[digest] = true
			checksumRecord = data
		}
	}
	if len(expected) == 0 {
		return nil, "", "", "", nil, fmt.Errorf("%w: release asset has no verifiable digest (spec digest, asset digest, or checksums required)", ErrInvalid)
	}

	payload, err := fetchReleaseBytes(ctx, client, asset.BrowserDownloadURL, cliReleaseMaxAssetBytes)
	if err != nil {
		return nil, "", "", "", nil, err
	}
	if int64(len(payload)) > cliReleaseMaxAssetBytes {
		return nil, "", "", "", nil, fmt.Errorf("%w: release asset exceeds %d bytes", ErrInvalid, cliReleaseMaxAssetBytes)
	}
	sum := sha256.Sum256(payload)
	got := "sha256:" + hex.EncodeToString(sum[:])
	for want := range expected {
		if got != want {
			return nil, "", "", "", nil, fmt.Errorf("%w: release asset digest mismatch", ErrStale)
		}
	}
	if checksumRecord == nil {
		checksumRecord = []byte(got + "  " + asset.Name + "\n")
	}

	member := source.AssetMember
	if member == "" && isArchiveAsset(asset.Name) {
		member = defaultMember
	}
	binary, err = extractReleaseBinary(asset.Name, payload, member)
	if err != nil {
		return nil, "", "", "", nil, err
	}
	return binary, asset.Name, got, release.TagName, checksumRecord, nil
}

func isChecksumAsset(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "checksums") || strings.Contains(lower, "sha256") || strings.HasSuffix(lower, ".sha256sum") || lower == "shasums.txt"
}

// checksumForAsset parses "<hex64>  <name>" / "<hex64> *<name>" lines.
func checksumForAsset(data []byte, name string) string {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return "sha256:" + fields[0]
			}
		}
	}
	return ""
}

func isArchiveAsset(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".zip")
}

// extractReleaseBinary returns the binary member of an archive asset, or the
// raw asset when it carries no archive suffix. member selects an exact member
// path; when empty the basename of the archive stem matches.
func extractReleaseBinary(assetName string, payload []byte, member string) ([]byte, error) {
	lower := strings.ToLower(assetName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		zr, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("%w: release asset gzip", ErrInvalid)
		}
		defer func() { _ = zr.Close() }()
		return extractTarMember(tar.NewReader(zr), member)
	case strings.HasSuffix(lower, ".tar"):
		return extractTarMember(tar.NewReader(bytes.NewReader(payload)), member)
	case strings.HasSuffix(lower, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			return nil, fmt.Errorf("%w: release asset zip", ErrInvalid)
		}
		return extractZipMember(zr, member)
	default:
		if member != "" && member != assetName {
			return nil, fmt.Errorf("%w: non-archive asset cannot satisfy member selection", ErrInvalid)
		}
		return payload, nil
	}
}

const cliReleaseMaxMemberBytes = 128 << 20

func memberMatch(member, want string) bool {
	if want == "" {
		return false
	}
	if member == want {
		return true
	}
	if !strings.Contains(want, "/") {
		return path.Base(member) == want
	}
	return false
}

func extractTarMember(reader *tar.Reader, want string) ([]byte, error) {
	var found []byte
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: release asset tar", ErrInvalid)
		}
		if h.Typeflag != tar.TypeReg || !memberMatch(h.Name, want) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: release archive has multiple matching members", ErrStale)
		}
		data, err := io.ReadAll(io.LimitReader(reader, cliReleaseMaxMemberBytes+1))
		if err != nil || int64(len(data)) > cliReleaseMaxMemberBytes {
			return nil, fmt.Errorf("%w: release member exceeds size bound", ErrInvalid)
		}
		found = data
	}
	if found == nil {
		return nil, fmt.Errorf("%w: release archive member not found", ErrNotFound)
	}
	return found, nil
}

func extractZipMember(reader *zip.Reader, want string) ([]byte, error) {
	var found []byte
	for _, f := range reader.File {
		if f.FileInfo().IsDir() || !memberMatch(f.Name, want) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("%w: release archive has multiple matching members", ErrStale)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%w: release asset zip member", ErrInvalid)
		}
		data, err := io.ReadAll(io.LimitReader(rc, cliReleaseMaxMemberBytes+1))
		_ = rc.Close()
		if err != nil || int64(len(data)) > cliReleaseMaxMemberBytes {
			return nil, fmt.Errorf("%w: release member exceeds size bound", ErrInvalid)
		}
		found = data
	}
	if found == nil {
		return nil, fmt.Errorf("%w: release archive member not found", ErrNotFound)
	}
	return found, nil
}

// generateCLIReleaseRecipe wraps the verified binary in a scratch image, or
// on a reviewed toolchain base when the binary needs a dynamic loader.
// The checksum record doubles as the dependency lock — it is the pin
// evidence for the mutable release source.
func generateCLIReleaseRecipe(binary string, binBytes, checksumRecord []byte, base string) (ArtifactRecipe, []byte, error) {
	if !validGuestBinaryPath(binary) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: cli binary guest path required", ErrInvalid)
	}
	if len(binBytes) == 0 || len(binBytes) > cliReleaseMaxMemberBytes {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: release binary size out of bounds", ErrInvalid)
	}
	if len(checksumRecord) == 0 || len(checksumRecord) > 4<<20 {
		checksumRecord = nil
	}
	if base != "" && !slices.Contains(slices.Collect(maps.Values(languageBaseImages)), base) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: cli release base must be a pinned toolchain image", ErrInvalid)
	}
	from := "scratch"
	if base != "" {
		from = base
	}
	argv, _ := json.Marshal([]string{binary})
	files := map[string][]byte{
		".hub/Dockerfile": []byte("FROM " + from + "\nCOPY --chmod=0755 tool " + binary + "\nUSER 10001:10001\nCMD []\nENTRYPOINT " + string(argv) + "\n"),
		"tool":            binBytes,
	}
	locks := []string{}
	if checksumRecord != nil {
		files["checksums.txt"] = checksumRecord
		locks = []string{"checksums.txt"}
	}
	var bases []string
	if base != "" {
		bases = []string{base}
	}
	recipe := ArtifactRecipe{Format: "cli-release-v1", Dockerfile: ".hub/Dockerfile", DependencyLocks: locks, BaseImages: bases, Entrypoint: []string{binary}}
	output, err := writeRecipeContext(&recipe, files)
	if err != nil {
		return ArtifactRecipe{}, nil, err
	}
	return recipe, output, recipe.VerifyContext(output)
}
