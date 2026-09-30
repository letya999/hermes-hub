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
	for _, scenario := range []string{"ok", "drift", "oversized", "status", "redirect", "symlink", "submodule", "duplicate", "empty", "extra-symlink", "asset"} {
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
					if scenario == "extra-symlink" {
						entries = append(entries, map[string]any{"path": "assets", "type": "blob", "mode": "120000", "sha": blobSHA, "size": 14})
					}
					if scenario == "asset" {
						entries = append(entries, map[string]any{"path": "images/demo.gif", "type": "blob", "mode": "100644", "sha": blobSHA, "size": len(content)})
					}
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
			data, err := fetchArtifactContextMode(context.Background(), client, ArtifactSource{Repository: "https://github.com/example/mcp", CommitSHA: sha}, nil, 64<<20, true, false, false)
			if scenario != "ok" && scenario != "extra-symlink" && scenario != "asset" {
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
	data, err := fetchArtifactContextMode(context.Background(), client, ArtifactSource{Repository: "https://github.com/example/mcp", Subfolder: "packages/mcp", CommitSHA: sha}, nil, 64<<20, true, false, false)
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

func TestRepositoryRecipeContextScopesPinnedSubfolderOnce(t *testing.T) {
	commit, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	dockerfile := []byte("FROM node:22\nENTRYPOINT [\"node\",\"server.js\"]\n")
	h := sha1.New() // #nosec G401 -- fixture Git blob ID.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(dockerfile))
	_, _ = h.Write(dockerfile)
	blobSHA := hex.EncodeToString(h.Sum(nil))
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/git/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": commit, "tree": map[string]any{"sha": treeSHA}})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": treeSHA, "tree": []any{
				map[string]any{"path": "packages/mcp/Dockerfile", "type": "blob", "mode": "100644", "sha": blobSHA, "size": len(dockerfile)},
			}})
		default:
			if r.URL.Path != "/example/mcp/"+commit+"/packages/mcp/Dockerfile" {
				t.Errorf("unexpected raw file %s", r.URL.Path)
			}
			_, _ = w.Write(dockerfile)
		}
	}))
	defer fixture.Close()
	client := &http.Client{Transport: calendarFixtureTransport{endpoint: fixture.URL, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	source := ArtifactSource{Repository: "https://github.com/example/mcp", Subfolder: "packages/mcp", CommitSHA: commit}
	contextBytes, err := fetchArtifactContextMode(context.Background(), client, source, nil, 64<<20, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := (RecipeResolver{}).Resolve(context.Background(), source, contextBytes)
	if err != nil || len(resolution.Launch.Entrypoint) != 2 || resolution.Launch.Entrypoint[0] != "node" {
		t.Fatalf("subfolder recipe=%+v err=%v", resolution, err)
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

func TestOverlayArtifactContextMergesPinnedTree(t *testing.T) {
	sha, treeSHA := strings.Repeat("a", 40), strings.Repeat("b", 40)
	guide, readme := []byte("# Guide\n"), []byte("docs repo\n")
	blobSHA := func(content []byte) string {
		h := sha1.New() // #nosec G401 -- fixture Git blob ID.
		_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
		_, _ = h.Write(content)
		return hex.EncodeToString(h.Sum(nil))
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/git/commits/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": sha, "tree": map[string]any{"sha": treeSHA}})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": treeSHA, "tree": []any{
				map[string]any{"path": "docs/guide.md", "type": "blob", "mode": "100644", "sha": blobSHA(guide), "size": len(guide)},
				map[string]any{"path": "README.md", "type": "blob", "mode": "100644", "sha": blobSHA(readme), "size": len(readme)},
				map[string]any{"path": ".env", "type": "blob", "mode": "100644", "sha": blobSHA(readme), "size": len(readme)},
				map[string]any{"path": "link", "type": "blob", "mode": "120000", "sha": blobSHA(readme), "size": len(readme)},
			}})
		default:
			// docs/ is pruned from primary build contexts; the overlay's full
			// tree mode must still fetch it.
			if r.URL.Path == "/example/docs/"+sha+"/docs/guide.md" {
				_, _ = w.Write(guide)
				return
			}
			if r.URL.Path == "/example/docs/"+sha+"/README.md" {
				_, _ = w.Write(readme)
				return
			}
			t.Errorf("unexpected raw file %s", r.URL.Path)
		}
	}))
	defer fixture.Close()
	client := &http.Client{Transport: calendarFixtureTransport{endpoint: fixture.URL, base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	source := ArtifactSource{Repository: "https://github.com/example/docs", CommitSHA: sha}
	var base bytes.Buffer
	w := tar.NewWriter(&base)
	_ = w.WriteHeader(&tar.Header{Name: "go.mod", Mode: 0644, Size: 9, Typeflag: tar.TypeReg})
	_, _ = w.Write([]byte("module x\n"))
	_ = w.WriteHeader(&tar.Header{Name: "main.go", Mode: 0644, Size: 7, Typeflag: tar.TypeReg})
	_, _ = w.Write([]byte("package"))
	_ = w.Close()

	merged, err := overlayArtifactContext(context.Background(), client, source, "cmd/x/external/docs", base.Bytes(), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string][]byte{}
	var order []string
	r := tar.NewReader(bytes.NewReader(merged))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		names[h.Name] = data
		order = append(order, h.Name)
	}
	for _, want := range []string{"go.mod", "main.go", "cmd/x/external/docs/docs/guide.md", "cmd/x/external/docs/README.md"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("merged context missing %q: %v", want, order)
		}
	}
	if !bytes.Equal(names["cmd/x/external/docs/docs/guide.md"], guide) {
		t.Fatal("overlay file content changed")
	}
	if _, ok := names["cmd/x/external/docs/.env"]; ok {
		t.Fatal("sensitive overlay file admitted")
	}
	if _, ok := names["cmd/x/external/docs/link"]; ok {
		t.Fatal("overlay symlink admitted")
	}
	if order[0] != "go.mod" || order[1] != "main.go" {
		t.Fatalf("overlay shadowed primary context order: %v", order)
	}
	if _, err := overlayArtifactContext(context.Background(), client, source, "cmd/x/external/docs", base.Bytes(), 64<<20); err != nil {
		t.Fatal("deterministic overlay merge failed")
	}

	var colliding bytes.Buffer
	w = tar.NewWriter(&colliding)
	_ = w.WriteHeader(&tar.Header{Name: "cmd/x/external/docs/README.md", Mode: 0644, Size: 4, Typeflag: tar.TypeReg})
	_, _ = w.Write([]byte("base"))
	_ = w.Close()
	for _, tc := range []struct {
		into     string
		base     []byte
		maxBytes int64
	}{
		{"", base.Bytes(), 64 << 20},
		{"../x", base.Bytes(), 64 << 20},
		{".hub/x", base.Bytes(), 64 << 20},
		{"a//b", base.Bytes(), 64 << 20},
		{"docs/secrets", base.Bytes(), 64 << 20},
		{"cmd/x/external/docs", nil, 64 << 20},
		{"cmd/x/external/docs", colliding.Bytes(), 64 << 20},
		{"cmd/x/external/docs", base.Bytes(), 1},
	} {
		if _, err := overlayArtifactContext(context.Background(), client, source, tc.into, tc.base, tc.maxBytes); err == nil {
			t.Fatalf("accepted unsafe overlay %+v", tc)
		}
	}
	if _, err := OverlayArtifactContext(context.Background(), ArtifactSource{Repository: "https://github.com/example/docs", CommitSHA: "main"}, "x", base.Bytes(), 64<<20); err == nil {
		t.Fatal("mutable overlay ref accepted")
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
