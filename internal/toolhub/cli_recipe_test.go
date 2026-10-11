package toolhub

import (
	"slices"
	"strings"
	"testing"
)

func cliRecipeDockerfile(t *testing.T, contextBytes []byte) string {
	t.Helper()
	return string(mustContextFile(t, contextBytes, ".hub/Dockerfile"))
}

func TestGenerateCLIRecipeLanguages(t *testing.T) {
	base := "example/toolchain@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		files  map[string]string
		binary string
		wantIn []string
		notIn  []string
		entry  []string
	}{
		{
			"go", map[string]string{"go.mod": "module example.org/tool", "go.sum": "", "cmd/tool/main.go": "package main\nfunc main() {}"},
			"/tool",
			[]string{"FROM " + base + " AS build", "FROM scratch", "COPY --from=build /app/tool /tool", "ca-certificates.crt", `ENTRYPOINT ["/tool"]`},
			[]string{"pip", "npm"},
			[]string{"/tool"},
		},
		{
			"rust", map[string]string{"Cargo.toml": "[package]\nname = 'fd'", "Cargo.lock": "", "src/main.rs": "fn main() {}"},
			"/usr/local/bin/fd",
			[]string{"FROM " + base + " AS build", "x86_64-unknown-linux-musl", "FROM scratch", "COPY --from=build /app/target/x86_64-unknown-linux-musl/release/fd /usr/local/bin/fd", `ENTRYPOINT ["/usr/local/bin/fd"]`},
			[]string{"npm"},
			[]string{"/usr/local/bin/fd"},
		},
		{
			"python", map[string]string{"pyproject.toml": "[project.scripts]\nmycli = 'cli:main'"},
			"/opt/venv/bin/mycli",
			[]string{"AS build", "venv", "FROM " + base, "COPY --from=build /opt/venv /opt/venv", "RUN test -x /opt/venv/bin/mycli", `ENTRYPOINT ["/opt/venv/bin/mycli"]`},
			[]string{"scratch"},
			[]string{"/opt/venv/bin/mycli"},
		},
		{
			"node-script", map[string]string{"package.json": `{"bin":{"cli":"dist/cli.js"}}`, "package-lock.json": "{}"},
			"/app/dist/cli.js",
			[]string{"AS build", "npm ci", "FROM " + base, "COPY --from=build /app /app", "RUN test -e /app/dist/cli.js", `ENTRYPOINT ["/usr/local/bin/node","/app/dist/cli.js"]`},
			[]string{"scratch"},
			[]string{"/usr/local/bin/node", "/app/dist/cli.js"},
		},
		{
			"node-bin-shim", map[string]string{"package.json": `{"bin":{"cli":"dist/cli.js"}}`, "package-lock.json": "{}"},
			"/app/node_modules/.bin/cli",
			[]string{"npm ci", "RUN test -e /app/node_modules/.bin/cli", `ENTRYPOINT ["/app/node_modules/.bin/cli"]`},
			nil,
			[]string{"/app/node_modules/.bin/cli"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recipe, contextBytes, err := GenerateCLIRecipe(languageContext(t, tc.files), "", base, tc.binary)
			if err != nil {
				t.Fatal(err)
			}
			dockerfile := cliRecipeDockerfile(t, contextBytes)
			for _, want := range tc.wantIn {
				if !strings.Contains(dockerfile, want) {
					t.Fatalf("dockerfile missing %q:\n%s", want, dockerfile)
				}
			}
			for _, unwanted := range tc.notIn {
				if strings.Contains(dockerfile, unwanted) {
					t.Fatalf("dockerfile must not contain %q:\n%s", unwanted, dockerfile)
				}
			}
			if !slices.Equal(recipe.Entrypoint, tc.entry) {
				t.Fatalf("entrypoint=%v", recipe.Entrypoint)
			}
			if !strings.Contains(dockerfile, "USER 10001:10001") {
				t.Fatalf("missing non-root user:\n%s", dockerfile)
			}
			if err := recipe.VerifyContext(contextBytes); err != nil {
				t.Fatalf("generated recipe failed verify: %v", err)
			}
		})
	}
}

func TestGenerateCLIRecipeRejects(t *testing.T) {
	base := "example/toolchain@sha256:" + strings.Repeat("a", 64)
	goFiles := map[string]string{"go.mod": "module example.org/tool", "go.sum": "", "cmd/tool/main.go": "package main\nfunc main() {}"}
	for _, tc := range []struct {
		name   string
		files  map[string]string
		binary string
		lang   string
	}{
		{"relative-binary", goFiles, "tool", ""},
		{"traversal", goFiles, "/x/../tool", ""},
		{"missing-manifest", map[string]string{"main.go": "package main"}, "/tool", ""},
		{"two-mains", map[string]string{"go.mod": "module m", "go.sum": "", "a/main.go": "package main", "b/main.go": "package main"}, "/tool", ""},
		{"rust-no-lock", map[string]string{"Cargo.toml": "[package]\nname = 'fd'", "src/main.rs": "fn main() {}"}, "/fd", ""},
		{"python-path", map[string]string{"pyproject.toml": "[project.scripts]\nx='a:b'"}, "/usr/bin/x", ""},
		{"node-path", map[string]string{"package.json": `{}`, "package-lock.json": "{}"}, "/usr/bin/x", ""},
		{"node-no-lock", map[string]string{"package.json": `{}`}, "/app/x.js", ""},
		{"unsupported", map[string]string{"Makefile": "all:"}, "/tool", ""},
		{"bad-language", goFiles, "/tool", "cobol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := GenerateCLIRecipe(languageContext(t, tc.files), tc.lang, base, tc.binary); err == nil {
				t.Fatal("spec admitted")
			}
		})
	}
}

func TestGenerateCLIRecipeDeterministic(t *testing.T) {
	base := "example/toolchain@sha256:" + strings.Repeat("b", 64)
	files := map[string]string{"go.mod": "module example.org/tool", "go.sum": "", "cmd/tool/main.go": "package main\nfunc main() {}"}
	_, first, err := GenerateCLIRecipe(languageContext(t, files), "", base, "/tool")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := GenerateCLIRecipe(languageContext(t, files), "", base, "/tool")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("nondeterministic cli recipe")
	}
}
