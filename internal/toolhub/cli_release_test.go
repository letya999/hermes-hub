package toolhub

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGitHubReleaseSource(t *testing.T) {
	source, err := parseGitHubReleaseSource("github-release:BurntSushi/ripgrep@14.1.1", "ripgrep-*-linux-musl.tar.gz", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository != "https://github.com/BurntSushi/ripgrep" || source.Tag != "14.1.1" || source.Asset != "ripgrep-*-linux-musl.tar.gz" {
		t.Fatalf("source=%+v", source)
	}
	if _, err := source.ArchiveURL(); err == nil {
		t.Fatal("release source must not resolve to a commit archive")
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	source, err = parseGitHubReleaseSource("github-release:o/r@1.2.3", "tool", digest, "dir/tool")
	if err != nil || source.AssetDigest != digest || source.AssetMember != "dir/tool" {
		t.Fatalf("digest/member source=%+v err=%v", source, err)
	}
	for _, tc := range []struct {
		raw, asset, digest, member string
	}{
		{"github-release:o/r", "a", "", ""},
		{"github-release:o@1.0", "a", "", ""},
		{"github-release:o/r@tag with space", "a", "", ""},
		{"github-release:o/r@../etc", "a", "", ""},
		{"github-release:o/r@1.0", "", "", ""},
		{"github-release:o/r@1.0", "../evil", "", ""},
		{"github-release:o/r@1.0", "a/b", "", ""},
		{"github-release:o/r@1.0", "a", "sha256:xyz", ""},
		{"github-release:o/r@1.0", "a", "", "../escape"},
	} {
		if _, err := parseGitHubReleaseSource(tc.raw, tc.asset, tc.digest, tc.member); !errors.Is(err, ErrInvalid) {
			t.Fatalf("source %q asset %q admitted: %v", tc.raw, tc.asset, err)
		}
	}
}

func tarGzFixture(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	w := tar.NewWriter(gz)
	for name, body := range members {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipFixture(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, body := range members {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestExtractReleaseBinary(t *testing.T) {
	tgz := tarGzFixture(t, map[string]string{"rg-14-x86_64/rg": "BIN-ELF", "rg-14-x86_64/README": "doc"})
	zipBytes := zipFixture(t, map[string]string{"fd/fd": "ZIP-BIN", "fd/doc": "d"})
	for _, tc := range []struct {
		asset, member string
		payload       []byte
		want          string
		err           error
	}{
		{"rg.tar.gz", "rg", tgz, "BIN-ELF", nil},
		{"rg.tgz", "rg-14-x86_64/rg", tgz, "BIN-ELF", nil},
		{"fd.zip", "fd", zipBytes, "ZIP-BIN", nil},
		{"raw-binary", "", []byte("RAW"), "RAW", nil},
		{"rg.tar.gz", "missing", tgz, "", ErrNotFound},
		{"rg.tar.gz", "README", tarGzFixture(t, map[string]string{"a/README": "1", "b/README": "2"}), "", ErrStale},
		{"fd.zip", "missing", zipBytes, "", ErrNotFound},
		{"fd.zip", "fd", zipFixture(t, map[string]string{"a/fd": "1", "b/fd": "2"}), "", ErrStale},
		{"fd.zip", "", zipBytes, "", ErrNotFound},
		{"raw-binary", "other", []byte("RAW"), "", ErrInvalid},
		{"bad.tgz", "", []byte("not-gzip"), "", ErrInvalid},
		{"bad.zip", "", []byte("not-zip"), "", ErrInvalid},
	} {
		got, err := extractReleaseBinary(tc.asset, tc.payload, tc.member)
		if !errors.Is(err, tc.err) || (err == nil && string(got) != tc.want) {
			t.Fatalf("extract(%s,%s)=%q err=%v", tc.asset, tc.member, got, err)
		}
	}
}

func TestChecksumForAsset(t *testing.T) {
	sum := sha256.Sum256([]byte("payload"))
	hexSum := hex.EncodeToString(sum[:])
	data := []byte(hexSum + "  tool-linux-amd64\n" + hexSum + " *tool-windows-amd64.zip\nnot-a-sha line\n")
	if got := checksumForAsset(data, "tool-linux-amd64"); got != "sha256:"+hexSum {
		t.Fatalf("got=%q", got)
	}
	if got := checksumForAsset(data, "tool-windows-amd64.zip"); got != "sha256:"+hexSum {
		t.Fatalf("star-form got=%q", got)
	}
	if got := checksumForAsset(data, "missing"); got != "" {
		t.Fatalf("missing=%q", got)
	}
}

func releaseServer(t *testing.T, assetName string, assetBody, checksums []byte, apiDigest string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(assetBody)
	var serverURL string
	var mux http.ServeMux
	mux.HandleFunc("/repos/BurntSushi/ripgrep/releases/tags/14.1.1", func(w http.ResponseWriter, _ *http.Request) {
		digest := ""
		if apiDigest != "" {
			digest = `"digest":"` + apiDigest + `",`
		}
		fmt.Fprintf(w, `{"tag_name":"14.1.1","assets":[{%s"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
			digest, assetName, serverURL+"/dl/asset", serverURL+"/dl/checksums")
	})
	mux.HandleFunc("/dl/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(assetBody) })
	mux.HandleFunc("/dl/checksums", func(w http.ResponseWriter, _ *http.Request) {
		if checksums == nil {
			fmt.Fprintf(w, "%x  %s\n", sum, assetName)
			return
		}
		_, _ = w.Write(checksums)
	})
	server := httptest.NewServer(&mux)
	t.Cleanup(server.Close)
	serverURL = server.URL
	return server
}

func TestFetchReleaseBinaryVerifiesDigests(t *testing.T) {
	asset := tarGzFixture(t, map[string]string{"rg-14/rg": "RG-BINARY"})
	sum := "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256(asset); return s[:] }())
	server := releaseServer(t, "rg-14.tar.gz", asset, nil, "")
	defer server.Close()
	source := ArtifactSource{Repository: "https://github.com/BurntSushi/ripgrep", Tag: "14.1.1", Asset: "rg-*"}
	binary, name, digest, sums, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "rg")
	if err != nil {
		t.Fatal(err)
	}
	if string(binary) != "RG-BINARY" || name != "rg-14.tar.gz" || digest != sum || len(sums) == 0 {
		t.Fatalf("binary=%q name=%s digest=%s", binary, name, digest)
	}

	// A spec-supplied digest is verified against the same payload.
	source.AssetDigest = sum
	if _, _, got, _, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "rg"); err != nil || got != sum {
		t.Fatalf("spec digest path: %v got=%s", err, got)
	}
	// ...and a wrong one fails closed.
	source.AssetDigest = "sha256:" + strings.Repeat("0", 64)
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "rg"); !errors.Is(err, ErrStale) {
		t.Fatalf("mutable asset admitted: %v", err)
	}
	source.AssetDigest = ""

	// API-provided digest participates too.
	server2 := releaseServer(t, "rg-14.tar.gz", asset, nil, sum)
	defer server2.Close()
	if _, _, got, _, _, err := fetchReleaseBinary(context.Background(), source, server2.Client(), server2.URL, "rg"); err != nil || got != sum {
		t.Fatalf("api digest path: %v", err)
	}
	server3 := releaseServer(t, "rg-14.tar.gz", asset, nil, "sha256:"+strings.Repeat("1", 64))
	defer server3.Close()
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), source, server3.Client(), server3.URL, "rg"); !errors.Is(err, ErrStale) {
		t.Fatalf("conflicting api digest admitted: %v", err)
	}
}

func TestFetchReleaseBinaryNoDigestFailsClosed(t *testing.T) {
	asset := []byte("RAWBIN")
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			fmt.Fprintf(w, `{"tag_name":"1.0","assets":[{"name":"tool","browser_download_url":%q}]}`, server.URL+"/dl/asset")
			return
		}
		_, _ = w.Write(asset)
	}))
	defer server.Close()
	source := ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0", Asset: "tool"}
	if _, _, _, _, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "tool"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("undigested asset admitted: %v", err)
	}
	// A spec digest alone is enough.
	source.AssetDigest = "sha256:" + hex.EncodeToString(func() []byte { s := sha256.Sum256(asset); return s[:] }())
	binary, _, _, _, _, err := fetchReleaseBinary(context.Background(), source, server.Client(), server.URL, "tool")
	if err != nil || string(binary) != "RAWBIN" {
		t.Fatalf("spec-digest-only path: %v", err)
	}
}

func TestGenerateCLIReleaseRecipe(t *testing.T) {
	recipe, contextBytes, err := generateCLIReleaseRecipe("/usr/local/bin/rg", []byte("ELF-BINARY"), []byte("abc123  rg.tar.gz\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Format != "cli-release-v1" || len(recipe.Entrypoint) != 1 || recipe.Entrypoint[0] != "/usr/local/bin/rg" {
		t.Fatalf("recipe=%+v", recipe)
	}
	if err := recipe.VerifyContext(contextBytes); err != nil {
		t.Fatalf("release recipe failed its own verify: %v", err)
	}
	dockerfile := string(mustContextFile(t, contextBytes, ".hub/Dockerfile"))
	if !strings.Contains(dockerfile, "FROM scratch") || !strings.Contains(dockerfile, "COPY --chmod=0755 tool /usr/local/bin/rg") || !strings.Contains(dockerfile, `ENTRYPOINT ["/usr/local/bin/rg"]`) {
		t.Fatalf("dockerfile=%s", dockerfile)
	}
	for _, bad := range []string{"relative/bin", "/x/../bin", ""} {
		if _, _, err := generateCLIReleaseRecipe(bad, []byte("x"), nil, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("binary %q admitted: %v", bad, err)
		}
	}
	// Dynamic binaries land on a pinned toolchain base, never a mutable tag.
	recipe, contextBytes, err = generateCLIReleaseRecipe("/usr/local/bin/duckdb", []byte("ELF-DYN"), []byte("def456  duckdb.zip\n"), languageBaseImages["python"])
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.BaseImages) != 1 || recipe.BaseImages[0] != languageBaseImages["python"] {
		t.Fatalf("base recipe=%+v", recipe)
	}
	if err := recipe.VerifyContext(contextBytes); err != nil {
		t.Fatalf("base release recipe failed its own verify: %v", err)
	}
	dockerfile = string(mustContextFile(t, contextBytes, ".hub/Dockerfile"))
	if !strings.Contains(dockerfile, "FROM "+languageBaseImages["python"]) {
		t.Fatalf("base dockerfile=%s", dockerfile)
	}
	if _, _, err := generateCLIReleaseRecipe("/usr/local/bin/x", []byte("x"), nil, "alpine:latest"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable base admitted: %v", err)
	}
}

func mustContextFile(t *testing.T, contextBytes []byte, name string) []byte {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(contextBytes))
	for {
		h, err := reader.Next()
		if err != nil {
			t.Fatalf("file %s not found", name)
		}
		if h.Name == name {
			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(reader); err != nil {
				t.Fatal(err)
			}
			return buf.Bytes()
		}
	}
}
