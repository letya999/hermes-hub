package toolhub

import (
	"archive/tar"
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func languageContext(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	for name, content := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestGenerateArtifactRecipeLanguages(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
		argv  []string
	}{
		{"python", map[string]string{"pyproject.toml": "[project.scripts]\nmcp-server = 'server:main'", "server.py": "def main(): pass"}, []string{"/opt/venv/bin/mcp-server"}},
		{"python-primary", map[string]string{"pyproject.toml": "[project]\nname = 'mcp-server'\n[project.scripts]\nmcp-server = 'server:main'\nsession-generator = 'session:main'"}, []string{"/opt/venv/bin/mcp-server"}},
		{"requirements", map[string]string{"requirements.txt": "mcp==1.0.0", "main.py": "print('fixture')"}, []string{"/opt/venv/bin/python", "/app/main.py"}},
		{"node", map[string]string{"package.json": `{"bin":"server.js","scripts":{"build":"tsc"}}`, "package-lock.json": "{}", "server.js": ""}, []string{"node", "/app/server.js"}},
		{"node-bin-map", map[string]string{"package.json": `{"bin":{"mcp":"./dist/server.js"}}`, "package-lock.json": "{}"}, []string{"node", "/app/dist/server.js"}},
		{"node-start", map[string]string{"package.json": `{"scripts":{"start":"node server.js"}}`, "package-lock.json": "{}"}, []string{"node", "/app/server.js"}},
		{"node-pnpm", map[string]string{"package.json": `{"bin":"server.js"}`, "pnpm-lock.yaml": "lockfileVersion: '9.0'"}, []string{"node", "/app/server.js"}},
		{"node-pnpm-workspace", map[string]string{"package.json": `{"scripts":{"build":"pnpm -r run build"}}`, "pnpm-lock.yaml": "lockfileVersion: '9.0'", "packages/mcp/package.json": `{"name":"@fixture/server-mcp","bin":{"mcp":"dist/index.js"}}`, "packages/sdk/package.json": `{"name":"@fixture/sdk"}`}, []string{"node", "/app/packages/mcp/dist/index.js"}},
		{"node-pnpm-workspace-build", map[string]string{"package.json": `{}`, "pnpm-lock.yaml": "", "packages/mcp/package.json": `{"name":"fixture-mcp","bin":"dist/index.js","scripts":{"build":"tsc"}}`, "packages/broken/package.json": `{`}, []string{"node", "/app/packages/mcp/dist/index.js"}},
		{"go", map[string]string{"go.mod": "module example.org/mcp", "go.sum": "", "cmd/mcp/main.go": "package main\nfunc main() {}", "cmd/mcp/tools.go": "package main"}, []string{"/app/mcp-server"}},
		{"rust", map[string]string{"Cargo.toml": "[package]\nname = 'mcp-server'", "Cargo.lock": "", "src/main.rs": "fn main() {}"}, []string{"/app/target/release/mcp-server"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, contextBytes, err := GenerateArtifactRecipe(languageContext(t, test.files), "", "example/toolchain@sha256:"+strings.Repeat("a", 64), nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.Entrypoint, test.argv) {
				t.Fatalf("argv = %v", r.Entrypoint)
			}
			if !bytes.Contains(contextBytes, []byte("CMD []\nENTRYPOINT ")) {
				t.Fatal("base image command not cleared")
			}
			if err := r.VerifyContext(contextBytes); err != nil {
				t.Fatal(err)
			}
			_, again, err := GenerateArtifactRecipe(languageContext(t, test.files), "", r.BaseImages[0], nil)
			if err != nil || !bytes.Equal(contextBytes, again) {
				t.Fatalf("nondeterministic recipe: %v", err)
			}
		})
	}
}

func TestGenerateArtifactRecipeSelectsPinnedBase(t *testing.T) {
	recipe, _, err := GenerateArtifactRecipe(languageContext(t, map[string]string{
		"package.json": `{"bin":"server.js"}`, "package-lock.json": "{}",
	}), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.BaseImages[0]; got != "node@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae" {
		t.Fatalf("base = %q", got)
	}
}

func TestDiscoverOpenAPIEgress(t *testing.T) {
	contextBytes := languageContext(t, map[string]string{
		"api/openapi.json": `{"servers":[{"url":"https://API.TestSvc.dev/v1"},{"url":"http://unsafe.example"},{"url":"https://user@unsafe.example"},{"url":"https://unsafe.example:443"},{"url":"relative"}]}`,
		"bad-openapi.json": `{`,
		"other.json":       `{"servers":[{"url":"https://ignored.example"}]}`,
	})
	if got := discoverOpenAPIEgress(contextBytes); !slices.Equal(got, []string{"api.testsvc.dev"}) {
		t.Fatalf("egress = %v", got)
	}
	many := map[string]string{}
	for i := range 33 {
		many[fmt.Sprintf("%d-openapi.json", i)] = fmt.Sprintf(`{"servers":[{"url":"https://api%d.testsvc.dev"}]}`, i)
	}
	if got := discoverOpenAPIEgress(languageContext(t, many)); got != nil {
		t.Fatalf("oversized egress = %v", got)
	}
}

func TestDiscoverRepositoryHTTPSLiteralEgress(t *testing.T) {
	contextBytes := languageContext(t, map[string]string{"src/server.ts": `const endpoint = "https://calendar.googleapis.com/calendar/v3"`})
	if got := discoverOpenAPIEgress(contextBytes); !slices.Equal(got, []string{"calendar.googleapis.com"}) {
		t.Fatalf("repository HTTPS egress=%v", got)
	}
}

func TestDiscoverRepositoryHTTPSLiteralFiltersFixtures(t *testing.T) {
	contextBytes := languageContext(t, map[string]string{
		"src/server.ts":          `const urls = ["https://api.real-service.io","https://attacker.example.com","https://api.","https://raw.hostname","https://EVIL.Example.org","https://127.0.0.1"]`,
		"src/server_test.go":     `const fixture = "https://api.github.com.attacker.example"`,
		"testdata/mock.ts":       `const mock = "https://fixture.real-domain.io"`,
		"pkg/__toolsnaps__/x.go": `const snap = "https://snap.real-domain.io"`,
	})
	if got := discoverOpenAPIEgress(contextBytes); !slices.Equal(got, []string{"api.real-service.io"}) {
		t.Fatalf("filtered egress=%v", got)
	}
}

func TestCredentialEgressAddsGoogleOAuthEndpoints(t *testing.T) {
	got := credentialEgress([]string{"www.googleapis.com"}, []CredentialInput{{Name: "GOOGLE_OAUTH_CREDENTIALS", Required: true}})
	if !slices.Equal(got, []string{"accounts.google.com", "oauth2.googleapis.com", "www.googleapis.com"}) {
		t.Fatalf("credential egress=%v", got)
	}
}

func TestGenerateArtifactRecipeGoDisambiguationUsesUpstreamMetadata(t *testing.T) {
	base := "example/toolchain@sha256:" + strings.Repeat("a", 64)
	upstream := "FROM golang AS build\n" +
		"RUN --mount=type=cache,target=/go/pkg/mod \\\n" +
		"    --mount=type=secret,id=oauth_client_id \\\n" +
		"    go build ./cmd/server-mcp\n" +
		"FROM gcr.io/distroless/base\n" +
		"COPY --from=build /build/server-mcp /server/server-mcp\n" +
		"ENTRYPOINT [\"/server/server-mcp\"]\n" +
		"CMD [\"stdio\"]\n"
	for _, test := range []struct {
		name  string
		files map[string]string
		argv  []string
		build string
	}{
		{
			"dockerfile-entrypoint-selects-main-and-args",
			map[string]string{
				"go.mod":                            "module example.org/server-mcp",
				"go.sum":                            "",
				"cmd/server-mcp/main.go":            "package main\nfunc main() {}",
				"cmd/mcpcurl/main.go":               "package main\nfunc main() {}",
				"script/print-diff-configs/main.go": "package main\nfunc main() {}",
				"Dockerfile":                        upstream,
			},
			[]string{"/app/mcp-server", "stdio"}, "./cmd/server-mcp",
		},
		{
			"module-basename-selects-main",
			map[string]string{
				"go.mod":                 "module example.org/server-mcp",
				"go.sum":                 "",
				"cmd/server-mcp/main.go": "package main",
				"cmd/mcpcurl/main.go":    "package main",
			},
			[]string{"/app/mcp-server"}, "./cmd/server-mcp",
		},
		{
			"cmd-without-entrypoint-supplies-args",
			map[string]string{
				"go.mod":           "module example.org/tool",
				"go.sum":           "",
				"cmd/tool/main.go": "package main",
				"Dockerfile":       "FROM base\nCMD [\"/server/tool\",\"--serve\"]\n",
			},
			[]string{"/app/mcp-server", "--serve"}, "./cmd/tool",
		},
		{
			"different-upstream-binary-keeps-plain-argv",
			map[string]string{
				"go.mod":           "module example.org/tool",
				"go.sum":           "",
				"cmd/tool/main.go": "package main",
				"Dockerfile":       "FROM base\nENTRYPOINT [\"/other/bin\"]\nCMD [\"stdio\"]\n",
			},
			[]string{"/app/mcp-server"}, "./cmd/tool",
		},
		{
			"shell-form-cmd-is-not-an-argument",
			map[string]string{
				"go.mod":           "module example.org/tool",
				"go.sum":           "",
				"cmd/tool/main.go": "package main",
				"Dockerfile":       "FROM base\nENTRYPOINT [\"/server/tool\"]\nCMD stdio\n",
			},
			[]string{"/app/mcp-server"}, "./cmd/tool",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recipe, contextBytes, err := GenerateArtifactRecipe(languageContext(t, test.files), "", base, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(recipe.Entrypoint, test.argv) {
				t.Fatalf("argv = %v", recipe.Entrypoint)
			}
			if !bytes.Contains(contextBytes, []byte(test.build)) {
				t.Fatalf("wrong main package built: %s", contextBytes)
			}
		})
	}
	for _, files := range []map[string]string{
		{"go.mod": "module example.org/mcp", "a/main.go": "package main", "b/main.go": "package main"},
		{"go.mod": "module example.org/mcp", "cmd/a/main.go": "package main", "cmd/b/main.go": "package main", "Dockerfile": "FROM base\nENTRYPOINT [\"/server/unrelated\"]\n"},
		{"go.mod": "module example.org/mcp", "cmd/a/main.go": "package main", "cmd/b/main.go": "package main", "Dockerfile": "FROM base\nENTRYPOINT /server/a\n"},
		{"go.mod": "module example.org/mcp", "x/main.go": "package main", "y/main.go": "package main", "Dockerfile": "FROM base\nCMD [\"/bin/true\"]\n"},
	} {
		if _, _, err := GenerateArtifactRecipe(languageContext(t, files), "", base, nil); err == nil {
			t.Fatalf("ambiguous mains accepted: %v", files)
		}
	}
}

func TestGenerateArtifactRecipeRejectsUnsafeOrAmbiguousInputs(t *testing.T) {
	base := "example/toolchain@sha256:" + strings.Repeat("a", 64)
	for _, files := range []map[string]string{
		{}, {".env": "not-a-secret", "requirements.txt": "", "server.py": ""},
		{"requirements.txt": "", "server.py": "", "main.py": ""},
		{"package.json": "{}"}, {"package.json": "{", "package-lock.json": "{}"},
		{"package.json": `{"scripts":{"start":"sh -c something"}}`, "package-lock.json": "{}"},
		{"package.json": `{}`, "pnpm-lock.yaml": "", "a/package.json": `{"name":"a-mcp","bin":"server.js"}`, "b/package.json": `{"name":"b-mcp","bin":"server.js"}`},
		{"package.json": `{}`, "pnpm-lock.yaml": "", "a/package.json": `{"name":"a-mcp"}`},
		{"Cargo.toml": "[package]\nname='mcp'", "src/main.rs": ""},
		{"Cargo.toml": "[package]\nname='mcp'\n[workspace]", "Cargo.lock": "", "src/main.rs": ""},
		{"go.mod": "", "main.go": "package main", "other/main.go": "package main"},
		{"go.mod": "", "main.go": "package other"},
		{"requirements.txt": "", "server.py": "", "package.json": "{}"},
		{"requirements.txt": "", "server.py": "", ".hub/Dockerfile": "FROM attacker"},
	} {
		if _, _, err := GenerateArtifactRecipe(languageContext(t, files), "", base, nil); err == nil {
			t.Fatalf("accepted %v", files)
		}
	}
	input := languageContext(t, map[string]string{"requirements.txt": "", "main.py": ""})
	for _, test := range []struct {
		language, base string
		argv           []string
	}{
		{"ruby", base, nil}, {"go", base, nil}, {"python", "python:latest", nil},
		{"python", base, []string{"sh", "-c", "something"}},
	} {
		if _, _, err := GenerateArtifactRecipe(input, test.language, test.base, test.argv); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
	if _, _, err := GenerateArtifactRecipe([]byte("invalid tar"), "python", base, nil); err == nil {
		t.Fatal("accepted invalid archive")
	}
	if _, _, err := GenerateArtifactRecipe(append(slices.Clone(input), []byte("unreviewed trailing bytes")...), "python", base, nil); err == nil {
		t.Fatal("accepted trailing archive data")
	}
	if _, _, err := GenerateArtifactRecipe(input, "python", base, []string{"/opt/venv/bin/python", "/app/main.py"}); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateArtifactRecipeRejectsNonRegularAndDuplicateFiles(t *testing.T) {
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeReg} {
		var buffer bytes.Buffer
		w := tar.NewWriter(&buffer)
		for range 2 {
			if err := w.WriteHeader(&tar.Header{Name: "requirements.txt", Mode: 0644, Typeflag: kind}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := GenerateArtifactRecipe(buffer.Bytes(), "python", "example/base@sha256:"+strings.Repeat("a", 64), []string{"server"}); err == nil {
			t.Fatal("accepted unsafe context")
		}
	}
}
