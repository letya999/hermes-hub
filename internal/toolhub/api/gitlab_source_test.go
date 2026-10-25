package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitLabContextPinsAndFiltersBlobs(t *testing.T) {
	sha := strings.Repeat("a", 40)
	body := "print('fixture')\n"
	h := sha1.New() // #nosec G401 -- fixture Git object ID.
	fmt.Fprintf(h, "blob %d\x00%s", len(body), body)
	blob := hex.EncodeToString(h.Sum(nil))
	for _, scenario := range []string{"ok", "explicit", "metadata", "full", "pagination", "digest", "budget", "missing", "symlink", "duplicate", "file-cap", "page-cap"} {
		t.Run(scenario, func(t *testing.T) {
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "gitlab.com" || r.Header.Get("Authorization") != "" {
					t.Fatalf("unsafe request: %s", r.URL)
				}
				var value any
				headers := make(http.Header)
				switch {
				case strings.HasSuffix(r.URL.Path, "/commits/"+sha):
					value = map[string]any{"id": sha}
				case strings.HasSuffix(r.URL.Path, "/tree"):
					if r.URL.Query().Get("ref") != sha {
						t.Fatal("mutable tree")
					}
					mode := "100644"
					if scenario == "symlink" {
						mode = "120000"
					}
					value = []any{map[string]any{"path": "mcp/server.py", "mode": mode, "type": "blob", "id": blob}, map[string]any{"path": "mcp/.env", "mode": "100644", "type": "blob", "id": strings.Repeat("b", 40)}}
					if scenario == "missing" {
						value = []any{}
					}
					if scenario == "page-cap" {
						value = []any{}
						headers.Set("X-Next-Page", "2")
					}
					if scenario == "duplicate" {
						value = append(value.([]any), value.([]any)[0])
					}
					if scenario == "file-cap" {
						var many []any
						for i := 0; i < 4097; i++ {
							many = append(many, map[string]any{"path": fmt.Sprintf("mcp/file%d.py", i), "mode": "100644", "type": "blob", "id": blob})
						}
						value = many
					}
					if scenario == "pagination" && r.URL.Query().Get("page") == "1" {
						value = []any{}
						headers.Set("X-Next-Page", "2")
					}
				case strings.HasSuffix(r.URL.Path, "/blobs/"+blob):
					content := body
					if scenario == "digest" {
						content = "wrong"
					}
					value = map[string]any{"sha": blob, "size": len(content), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(content))}
				default:
					t.Fatalf("unexpected or sensitive blob request: %s", r.URL)
				}
				data, _ := json.Marshal(value)
				return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}
			limit := int64(1024)
			if scenario == "budget" {
				limit = 2
			}
			var files []string
			if scenario == "explicit" {
				files = []string{"mcp/server.py"}
			}
			data, err := FetchArtifactContextMode(t.Context(), client, ArtifactSource{Repository: "https://gitlab.com/team/subgroup/server", CommitSHA: sha, Subfolder: "mcp"}, files, limit, scenario != "explicit", scenario == "metadata", scenario == "full")
			if scenario == "digest" || scenario == "budget" || scenario == "missing" || scenario == "symlink" || scenario == "duplicate" || scenario == "file-cap" || scenario == "page-cap" {
				if err == nil {
					t.Fatal("invalid context admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reader := tar.NewReader(bytes.NewReader(data))
			header, err := reader.Next()
			expected := "server.py"
			if scenario == "explicit" || scenario == "metadata" {
				expected = "mcp/server.py"
			}
			if err != nil || header.Name != expected {
				t.Fatalf("context header: %+v %v", header, err)
			}
			content, _ := io.ReadAll(reader)
			if string(content) != body {
				t.Fatal("body mismatch")
			}
			if _, err := reader.Next(); err != io.EOF {
				t.Fatal("extra file")
			}
		})
	}
}

func TestGitLabSourcePinsPublicProject(t *testing.T) {
	sha := strings.Repeat("a", 40)
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "gitlab.com" || r.URL.EscapedPath() != "/api/v4/projects/team%2Fsubgroup%2Fserver/repository/commits/HEAD" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unsafe GitLab resolution: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"` + sha + `"}`))}, nil
	})}
	source, err := ResolveGitHubSourceWith(context.Background(), "https://gitlab.com/team/subgroup/server", client)
	if err != nil || source.Repository != "https://gitlab.com/team/subgroup/server" || source.CommitSHA != sha {
		t.Fatalf("GitLab source: %+v %v", source, err)
	}
	if _, err := source.ArchiveURL(); err != nil {
		t.Fatal(err)
	}
	pinned, err := ParseGitHubSource(source.Repository + "/-/tree/" + sha + "/mcp")
	if err != nil || pinned.Subfolder != "mcp" || pinned.CommitSHA != sha {
		t.Fatalf("GitLab pinned subfolder: %+v %v", pinned, err)
	}
	for _, raw := range []string{"https://token@gitlab.com/a/b", "https://gitlab.com/a/b?token=x", "http://gitlab.com/a/b", "https://gitlab.com:443/a/b", "https://gitlab.com/a/../b", "https://gitlab.com/a/%62", "https://gitlab.example.com/a/b", "https://gitlab.com/a/b/-/tree/main"} {
		if _, err := ResolveGitHubSourceWith(t.Context(), raw, client); err == nil {
			t.Fatalf("unsafe/mutable source accepted: %s", raw)
		}
	}
}

func TestGitLabSourceMetadata(t *testing.T) {
	source := ArtifactSource{Repository: "https://gitlab.com/team/subgroup/server", CommitSHA: strings.Repeat("a", 40), Subfolder: "mcp"}
	lookup, err := LookupRecipe(source)
	if err != nil || lookup.Owner != "team/subgroup" || lookup.Name != "server" {
		t.Fatalf("lookup: %+v %v", lookup, err)
	}
	if got := DefaultSelfInstallConfig(source).DefinitionID; got != "server" {
		t.Fatalf("definition ID: %s", got)
	}
	candidate := DiscoveryCandidate{Source: source}
	parsed, err := ParseGitHubSource(candidate.SourceURL())
	if err != nil || parsed != source {
		t.Fatalf("source round trip: %+v %v", parsed, err)
	}
}
