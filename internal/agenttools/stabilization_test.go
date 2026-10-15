package agenttools

import (
	"archive/zip"
	"bytes"
	"context"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM10ScopedArchiveAndEverySource(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/a.txt", "allowed")
	write(t, v, "secret.txt", "private")
	scopes := []api.CapabilityScope{{Resource: "files", PathArgument: "paths", PathPrefix: "docs"}}
	out, err := v.ExecCall(context.Background(), "archive_create", map[string]any{"path": "artifacts/one.zip", "paths": []string{"docs/a.txt"}}, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["path"] != "artifacts/one.zip" {
		t.Fatal(out)
	}
	f, err := v.Workspace.Open("artifacts/one.zip")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "docs/a.txt" {
		t.Fatalf("archive names=%+v %v", zr, err)
	}
	if _, err = v.ExecCall(context.Background(), "archive_create", map[string]any{"path": "artifacts/bad.zip", "paths": []string{"docs/a.txt", "secret.txt"}}, scopes); err == nil {
		t.Fatal("ungranted archive source accepted")
	}
	if _, err = v.Workspace.Stat("artifacts/bad.zip"); err == nil {
		t.Fatal("rejected call left an output")
	}
	if _, err = v.ExecCall(context.Background(), "image_inspect", map[string]any{"path": "docs/a.txt", "paths": []string{"secret.txt"}}, nil); err == nil {
		t.Fatal("single path tool accepted unchecked array")
	}
}

func TestM10InboxRetryAcknowledgesWithoutRefetch(t *testing.T) {
	v := fixture(t)
	fetches, acks := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fetches++
			w.Header().Set("X-Hub-Name", "note.txt")
			_, _ = w.Write([]byte("original"))
			return
		}
		acks++
		if acks == 1 {
			w.WriteHeader(503)
		}
	}))
	defer api.Close()
	v.CommunicationURL = api.URL
	first, err := v.inboxFetch(context.Background(), v.Workspace, Input{ID: "retry"})
	if err != nil || first["acknowledged"] != false {
		t.Fatalf("first=%v %v", first, err)
	}
	second, err := v.inboxFetch(context.Background(), v.Workspace, Input{ID: "retry"})
	if err != nil || second["acknowledged"] != true || fetches != 1 || acks != 2 {
		t.Fatalf("retry=%v fetches=%d acks=%d %v", second, fetches, acks, err)
	}
	body, err := v.Workspace.ReadFile("inbox/retry/files/note.txt")
	if err != nil || string(body) != "original" {
		t.Fatalf("original=%q %v", body, err)
	}
	if err = v.Workspace.MkdirAll("inbox/conflict", 0700); err != nil {
		t.Fatal(err)
	}
	if err = v.Workspace.WriteFile("inbox/conflict/keep.txt", []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = v.inboxFetch(context.Background(), v.Workspace, Input{ID: "conflict"}); err == nil {
		t.Fatal("preexisting incomplete directory replaced")
	}
	body, err = v.Workspace.ReadFile("inbox/conflict/keep.txt")
	if err != nil || !strings.EqualFold(string(body), "keep") {
		t.Fatal("preexisting data removed")
	}
}

func TestM10SeparateArchiveOutputScopeAndArrayBoundaries(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/a.txt", "allowed")
	if err := v.Workspace.MkdirAll("artifacts/exports", 0700); err != nil {
		t.Fatal(err)
	}
	scopes := []api.CapabilityScope{{Resource: "files", PathArgument: "paths", PathPrefix: "docs"}, {Resource: "artifacts", PathArgument: "path", PathPrefix: "artifacts/exports"}}
	out, err := v.ExecCall(context.Background(), "archive_create", map[string]any{"path": "artifacts/exports/one.zip", "paths": []string{"docs/a.txt"}}, scopes)
	if err != nil || out.(map[string]any)["path"] != "artifacts/exports/one.zip" {
		t.Fatalf("separate roots=%v %v", out, err)
	}
	if _, err = v.Workspace.Stat("docs/artifacts/exports/one.zip"); err == nil {
		t.Fatal("output misplaced under input scope")
	}
	for _, test := range []struct {
		args   map[string]any
		scopes []api.CapabilityScope
	}{
		{map[string]any{"path": "artifacts/exports/two.zip"}, scopes},
		{map[string]any{"path": "artifacts/exports/two.zip", "paths": []string{"docs/a.txt"}}, append(scopes, api.CapabilityScope{Resource: "files", PathArgument: "paths", PathPrefix: "other"})},
		{map[string]any{"path": "artifacts/exports/two.zip", "paths": []string{"docs/a.txt"}}, []api.CapabilityScope{{Resource: "files", PathArgument: "paths", PathPrefix: "missing"}}},
	} {
		if _, err = v.ExecCall(context.Background(), "archive_create", test.args, test.scopes); err == nil {
			t.Fatal("invalid array scope accepted")
		}
	}
	if _, err = v.ExecCall(context.Background(), "images_inspect", map[string]any{"paths": []string{"docs/a.txt"}}, []api.CapabilityScope{{Resource: "images", PathArgument: "paths", PathPrefix: "docs"}}); err == nil {
		t.Fatal("non-image accepted")
	}
	if _, err = v.ExecCall(context.Background(), "archive_create", map[string]any{"path": "artifacts/exports/one.zip", "paths": []string{"docs/a.txt"}}, scopes); err == nil {
		t.Fatal("existing archive overwritten")
	}
}

func TestM10ScopeDirectoryCannotAliasUnadmittedDirectory(t *testing.T) {
	v := fixture(t)
	write(t, v, "private/secret.txt", "private")
	// Even a regular file masquerading as a scope directory is refused.
	write(t, v, "not-directory", "file")
	if _, _, err := ScopedSub(v.Workspace, "not-directory", "not-directory/secret.txt"); err == nil {
		t.Fatal("non-directory scope opened")
	}
	if _, _, err := ScopedSub(v.Workspace, "private", "elsewhere/secret.txt"); err == nil {
		t.Fatal("actual path did not belong to its scope")
	}
	if err := os.Symlink(filepath.Join(v.Workspace.Name(), "private"), filepath.Join(v.Workspace.Name(), "alias")); err != nil {
		t.Skip("symlink creation unavailable on this host")
	}
	if _, err := v.ExecCall(context.Background(), "file_read", map[string]any{"path": "alias/secret.txt"}, []api.CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "alias"}}); err == nil {
		t.Fatal("scope symlink exposed private directory")
	}
	if _, err := v.ExecCall(context.Background(), "archive_create", map[string]any{"path": "artifacts/leak.zip", "paths": []string{"alias/secret.txt"}}, []api.CapabilityScope{{Resource: "files", PathArgument: "paths", PathPrefix: "alias"}}); err == nil {
		t.Fatal("archive read through a scope alias")
	}
}

func TestM10ExactDirectoryScopeKeepsItsOwnRoot(t *testing.T) {
	v := fixture(t)
	write(t, v, "docs/allowed.txt", "allowed")
	write(t, v, "private/secret.txt", "secret")
	sub, rel, err := ScopedSub(v.Workspace, "docs", "docs")
	if err != nil || rel != "" {
		t.Fatalf("directory scope=%q %v", rel, err)
	}
	defer sub.Close()
	if _, err := sub.ReadFile("../private/secret.txt"); err == nil {
		t.Fatal("directory scope opened its parent")
	}
	if body, err := sub.ReadFile("allowed.txt"); err != nil || string(body) != "allowed" {
		t.Fatalf("allowed=%q %v", body, err)
	}
	if err := os.Symlink(filepath.Join(v.Workspace.Name(), "private", "secret.txt"), filepath.Join(v.Workspace.Name(), "docs", "alias.txt")); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if _, _, err := ScopedSub(v.Workspace, "docs/alias.txt", "docs/alias.txt"); err == nil {
		t.Fatal("exact file grant followed alias")
	}
	if _, err := sub.ReadFile("alias.txt"); err == nil {
		t.Fatal("directory grant followed alias outside scope")
	}
}

func TestM10InboundFailuresNeverAcknowledgeOrReplaceFiles(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		file, body string
	}{{"missing", 404, "file.txt", ""}, {"unsafe", 200, ".", "data"}, {"badzip", 200, "broken.zip", "not a zip"}, {"large", 200, "file.txt", strings.Repeat("x", 9<<20+1)}} {
		t.Run(test.name, func(t *testing.T) {
			v := fixture(t)
			acks := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					acks++
					return
				}
				w.Header().Set("X-Hub-Name", test.file)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer api.Close()
			v.CommunicationURL = api.URL
			v.HTTP = nil
			if _, err := v.inboxFetch(context.Background(), v.Workspace, Input{ID: "failed"}); err == nil {
				t.Fatal("bad incoming file accepted")
			}
			if acks != 0 {
				t.Fatal("failed transfer acknowledged")
			}
			if _, err := v.Workspace.Stat("inbox/failed"); !os.IsNotExist(err) {
				t.Fatalf("partial output left: %v", err)
			}
		})
	}
	v := fixture(t)
	v.CommunicationURL = "http://unused"
	if err := v.Workspace.MkdirAll("inbox/corrupt", 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"broken", "null"} {
		if err := v.Workspace.WriteFile("inbox/corrupt/receipt.json", []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := v.inboxFetch(context.Background(), v.Workspace, Input{ID: "corrupt"}); err == nil {
			t.Fatal("corrupt receipt accepted")
		}
	}
}

func TestM10NegativeArchiveLimitFailsBeforeCreatingOutput(t *testing.T) {
	v := fixture(t)
	data := zipBytes(t, map[string]string{"file.txt": "data"})
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	old := maxUnpackBytes
	maxUnpackBytes = -1
	t.Cleanup(func() { maxUnpackBytes = old })
	if _, err := unpackZip(v.Workspace, zr, "inbox/negative"); err == nil {
		t.Fatal("negative byte limit accepted")
	}
	if _, err := v.Workspace.Stat("inbox/negative"); !os.IsNotExist(err) {
		t.Fatalf("failed extraction created output: %v", err)
	}
}
