package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1" // #nosec G505 -- fixture Git object ID.
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactSourcePinsGitSHA(t *testing.T) {
	s := ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: strings.Repeat("a", 40)}
	if got, err := s.ArchiveURL(); err != nil || !strings.HasSuffix(got, s.CommitSHA) {
		t.Fatalf("pinned source: %s %v", got, err)
	}
	for _, sha := range []string{"main", "v1.0.0", "latest", strings.Repeat("a", 39), strings.Repeat("A", 40), "--upload-pack=sh"} {
		s.CommitSHA = sha
		if _, err := s.ArchiveURL(); err == nil {
			t.Fatalf("accepted mutable/invalid SHA %q", sha)
		}
	}
	s.CommitSHA = strings.Repeat("a", 40)
	for _, repository := range []string{"http://github.com/a/b", "https://token@github.com/a/b", "https://github.com/a/b?token=x", "https://github.com/a/b#main", "https://github.com/a/b/tree/main", "https://github.com/a/../b", "https://github.com/a/b.git", "https://github.com/a/%62", "https://evil.example/a/b", "https://github.com:443/a/b"} {
		s.Repository = repository
		if _, err := s.ArchiveURL(); err == nil {
			t.Fatalf("accepted repository %q", repository)
		}
	}
}

func TestResolveGitHubSourcePinsDefaultBranch(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/repos/makenotion/notion-mcp-server/commits/HEAD" || request.Header.Get("X-GitHub-Api-Version") == "" {
			t.Fatalf("unexpected request: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"sha":"` + sha + `"}`)), Header: make(http.Header)}, nil
	})}
	source, err := resolveGitHubSource(context.Background(), "https://github.com/makenotion/notion-mcp-server", client)
	if err != nil || source.Repository != "https://github.com/makenotion/notion-mcp-server" || source.CommitSHA != sha {
		t.Fatalf("source=%+v err=%v", source, err)
	}
	for _, raw := range []string{"http://github.com/a/b", "https://github.com/a/b/tree/main", "https://token@github.com/a/b", "https://github.com/a/b?ref=main"} {
		if _, err := resolveGitHubSource(context.Background(), raw, client); err == nil {
			t.Fatalf("accepted mutable/unsafe repository %q", raw)
		}
	}
}

func TestArtifactAutomaticContextUsesVerifiedRawFiles(t *testing.T) {
	sha, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	content := "print('fixture')\n"
	h := sha1.New() // #nosec G401 -- fixture Git object ID.
	_, _ = fmt.Fprintf(h, "blob %d\x00%s", len(content), content)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	for _, scenario := range []string{"ok", "drift", "oversized", "status", "redirect", "symlink", "submodule", "duplicate", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("inherited credentials")
				}
				switch {
				case strings.Contains(r.URL.Path, "/git/commits/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": map[string]any{"sha": treeSHA}})
				case strings.Contains(r.URL.Path, "/git/trees/"):
					entry := map[string]any{"path": "server.py", "type": "blob", "mode": "100644", "sha": blobSHA, "size": len(content)}
					if scenario == "symlink" {
						entry["mode"] = "120000"
					}
					if scenario == "submodule" {
						entry["type"] = "commit"
					}
					entries := []any{entry, map[string]any{"path": ".env", "type": "blob", "mode": "100644", "sha": treeSHA, "size": 1000}, map[string]any{"path": "src", "type": "tree", "mode": "040000", "sha": treeSHA}}
					if scenario == "duplicate" {
						entries = append(entries, entry)
					}
					if scenario == "empty" {
						entries = entries[1:]
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"sha": treeSHA, "tree": entries})
				default:
					if r.URL.Path != "/example/mcp/"+sha+"/server.py" {
						t.Errorf("unexpected raw file %s", r.URL.Path)
					}
					if scenario == "status" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if scenario == "redirect" {
						http.Redirect(w, r, "/unexpected", http.StatusFound)
						return
					}
					body := content
					if scenario == "drift" {
						body = strings.Repeat("x", len(content))
					}
					if scenario == "oversized" {
						body += "extra"
					}
					_, _ = io.WriteString(w, body)
				}
			}))
			defer fixture.Close()
			client := &http.Client{Transport: calendarFixtureTransport{endpoint: fixture.URL, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			data, err := fetchArtifactContextMode(context.Background(), client, ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: sha}, nil, 64<<20, true, false)
			if scenario != "ok" {
				if err == nil || data != nil {
					t.Fatal("unsafe automatic source accepted")
				}
				return
			}
			if err != nil || requests != 3 {
				t.Fatalf("requests=%d err=%v", requests, err)
			}
			r := tar.NewReader(bytes.NewReader(data))
			header, err := r.Next()
			if err != nil || header.Name != "server.py" {
				t.Fatalf("wrong file: %v %v", header, err)
			}
			got, _ := io.ReadAll(r)
			if string(got) != content {
				t.Fatal("raw file changed")
			}
			if _, err := r.Next(); err != io.EOF {
				t.Fatal("excluded file included")
			}
		})
	}
	if _, err := FetchRepositoryArtifactContext(context.Background(), ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: "main"}, 1024); err == nil {
		t.Fatal("mutable ref accepted")
	}
}

func TestArtifactAutomaticContextScopesPinnedSubfolder(t *testing.T) {
	sha, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	content := []byte("print('scoped')\n")
	h := sha1.New() // #nosec G401 -- fixture Git blob ID.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/git/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": map[string]any{"sha": treeSHA}})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": treeSHA, "tree": []any{
				map[string]any{"path": "packages/mcp/server.py", "type": "blob", "mode": "100644", "sha": blobSHA, "size": len(content)},
				map[string]any{"path": "other/server.py", "type": "blob", "mode": "100644", "sha": blobSHA, "size": len(content)},
			}})
		default:
			if r.URL.Path != "/example/mcp/"+sha+"/packages/mcp/server.py" {
				t.Errorf("unexpected raw file %s", r.URL.Path)
			}
			_, _ = w.Write(content)
		}
	}))
	defer fixture.Close()
	client := &http.Client{Transport: calendarFixtureTransport{endpoint: fixture.URL, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	data, err := fetchArtifactContextMode(context.Background(), client, ArtifactSource{Repository: "https://github.com/example/mcp", Subfolder: "packages/mcp", CommitSHA: sha}, nil, 64<<20, true, false)
	if err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(bytes.NewReader(data))
	header, err := r.Next()
	if err != nil || header.Name != "server.py" {
		t.Fatalf("wrong scoped file: %+v %v", header, err)
	}
	if got, _ := io.ReadAll(r); !bytes.Equal(got, content) {
		t.Fatal("scoped file content changed")
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatal("file outside pinned subfolder included")
	}
}

func TestArtifactFetchVerifiesSelectedBlobs(t *testing.T) {
	sha, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	content := []byte("print('fixture')\n")
	h := sha1.New() // #nosec G401 -- fixture Git object ID.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = h.Write(content)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	for _, scenario := range []string{"ok", "commit-status", "commit-json", "commit-oversized-response", "commit-drift", "tree-status", "tree-drift", "tree-truncated", "missing", "symlink", "submodule", "duplicate", "oversized", "blob-status", "blob-json", "blob-encoding", "blob-id", "blob-size", "blob-content", "blob-digest", "redirect", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-GitHub-Api-Version") == "" {
					t.Error("source request inherited credentials or omitted API version")
				}
				stage := strings.Split(r.URL.Path, "/")[5]
				if scenario == "commit-oversized-response" {
					_, _ = io.WriteString(w, strings.Repeat(" ", (1<<20)+1))
					return
				}
				if scenario == strings.TrimSuffix(stage, "s")+"-status" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, "sensitive-upstream-body")
					return
				}
				if scenario == strings.TrimSuffix(stage, "s")+"-json" {
					_, _ = io.WriteString(w, "malformed")
					return
				}
				if scenario == "redirect" {
					http.Redirect(w, r, "/unexpected", http.StatusFound)
					return
				}
				var response any
				switch stage {
				case "commits":
					commitID := sha
					if scenario == "commit-drift" {
						commitID = treeSHA
					}
					response = map[string]any{"sha": commitID, "tree": map[string]any{"sha": treeSHA}}
				case "trees":
					if r.URL.RawQuery != "recursive=1" {
						t.Error("tree request lost its bounded query")
					}
					entry := map[string]any{"path": "server.py", "mode": "100644", "type": "blob", "sha": blobSHA, "size": len(content)}
					if scenario == "symlink" {
						entry["mode"] = "120000"
					}
					if scenario == "submodule" {
						entry["type"] = "commit"
					}
					if scenario == "oversized" {
						entry["size"] = len(content) + 1
					}
					entries := []any{entry, map[string]any{"path": "unrelated-private.txt", "mode": "100644", "type": "blob", "sha": treeSHA, "size": 1024}}
					if scenario == "missing" {
						entries = nil
					}
					if scenario == "duplicate" {
						entries = append(entries, entry)
					}
					id := treeSHA
					if scenario == "tree-drift" {
						id = sha
					}
					response = map[string]any{"sha": id, "truncated": scenario == "tree-truncated", "tree": entries}
				case "blobs":
					if !strings.HasSuffix(r.URL.Path, blobSHA) {
						t.Error("fetched undeclared blob")
					}
					blob := map[string]any{"sha": blobSHA, "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)}
					if scenario == "blob-encoding" {
						blob["encoding"] = "utf-8"
					}
					if scenario == "blob-id" {
						blob["sha"] = sha
					}
					if scenario == "blob-size" {
						blob["size"] = 0
					}
					if scenario == "blob-content" {
						blob["content"] = "invalid-base64!"
					}
					if scenario == "blob-digest" {
						blob["content"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, len(content)))
					}
					response = blob
				default:
					t.Errorf("unexpected source request %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer fixture.Close()
			client := &http.Client{Transport: calendarFixtureTransport{endpoint: fixture.URL, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			data, err := fetchArtifactContext(ctx, client, ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: sha}, []string{"server.py"}, int64(len(content)))
			if scenario != "ok" {
				if err == nil || data != nil {
					t.Fatal("unsafe source produced a context")
				}
				if strings.Contains(err.Error(), "sensitive-upstream-body") {
					t.Fatal("source error leaked response body")
				}
				return
			}
			if err != nil || requests != 3 {
				t.Fatalf("fetch failed: %v requests=%d", err, requests)
			}
			r := tar.NewReader(bytes.NewReader(data))
			header, err := r.Next()
			if err != nil || header.Name != "server.py" || header.Mode != 0644 {
				t.Fatalf("context metadata %+v %v", header, err)
			}
			got, _ := io.ReadAll(r)
			if !bytes.Equal(got, content) {
				t.Fatal("context content changed")
			}
		})
	}
	for _, files := range [][]string{nil, {"../x"}, {"server.py", "server.py"}} {
		if _, err := fetchArtifactContext(context.Background(), nil, ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: sha}, files, 100); err == nil {
			t.Fatal("invalid context attempted source fetch")
		}
	}
	if _, err := FetchArtifactContext(context.Background(), ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: "main"}, []string{"server.py"}, 100); err == nil {
		t.Fatal("mutable source attempted fetch")
	}
}

func TestArtifactContextIsolation(t *testing.T) {
	for _, name := range []string{"../x", "/x", "a/../x", "a\\x", "C:x", ".env", ".env.example", ".git/config", "spaces/alice/file", "a/secret.json", "credentials.json", "id_ed25519", ".npmrc", ".netrc", "a/key.pem", "a/token.json", ".mcp.json"} {
		if artifactContextPath(name) {
			t.Fatalf("accepted sensitive path %q", name)
		}
	}
	makeArchive := func(kind byte, duplicate bool) []byte {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		h := &tar.Header{Name: "repo-sha/main.py", Typeflag: kind, Mode: 0777}
		if kind == tar.TypeReg {
			h.Size = 4
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			_, _ = w.Write([]byte("test"))
		}
		if duplicate {
			_ = w.WriteHeader(h)
			_, _ = w.Write([]byte("test"))
		}
		_ = w.Close()
		return buf.Bytes()
	}
	var dst bytes.Buffer
	if err := CopyArtifactContext(&dst, bytes.NewReader(makeArchive(tar.TypeReg, false)), "repo-sha", []string{"main.py"}, 4); err != nil {
		t.Fatal(err)
	}
	r := tar.NewReader(&dst)
	h, err := r.Next()
	if err != nil || h.Name != "main.py" || h.Mode != 0644 || h.Uid != 0 {
		t.Fatalf("unsanitized context: %+v %v", h, err)
	}
	for _, tc := range []struct {
		kind      byte
		duplicate bool
		files     []string
		limit     int64
	}{
		{tar.TypeSymlink, false, []string{"main.py"}, 4},
		{tar.TypeReg, true, []string{"main.py"}, 8},
		{tar.TypeReg, false, []string{"main.py"}, 3},
		{tar.TypeReg, false, []string{"missing"}, 4},
		{tar.TypeReg, false, []string{"../x"}, 4},
		{tar.TypeReg, false, []string{"main.py", "main.py"}, 4},
		{tar.TypeReg, false, nil, 4},
		{tar.TypeReg, false, []string{"main.py"}, 0},
	} {
		if err := CopyArtifactContext(&bytes.Buffer{}, bytes.NewReader(makeArchive(tc.kind, tc.duplicate)), "repo-sha", tc.files, tc.limit); err == nil {
			t.Fatalf("accepted unsafe context %+v", tc)
		}
	}
	if err := CopyArtifactContext(&bytes.Buffer{}, strings.NewReader("malformed"), "repo-sha", []string{"main.py"}, 4); err == nil {
		t.Fatal("accepted malformed archive")
	}
}
