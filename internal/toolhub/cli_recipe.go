package toolhub

// CLI-mode recipe generation: a bounded-cli artifact is a single executable,
// not a long-running MCP server, so the image is multi-stage — a pinned
// toolchain stage builds the tool and a minimal runtime stage carries just
// the artifact. Static toolchains (Go, Rust musl) land on scratch plus
// ca-certificates; Python keeps its /opt/venv and Node keeps /app with
// node_modules on the same pinned base. The recipe is still ours: upstream
// Dockerfiles and install scripts never run.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// validGuestBinaryPath validates the declared tool path inside the built
// image. Slash semantics; the cell never treats it as a host path.
func validGuestBinaryPath(binary string) bool {
	return strings.HasPrefix(binary, "/") && len(binary) <= 256 && !strings.Contains(binary, "..") && !strings.ContainsAny(binary, "\\\x00\r\n") && !strings.Contains(binary, "//")
}

var cliBinaryScriptPattern = regexp.MustCompile(`\.(js|mjs|cjs)$`)

// GenerateCLIRecipe builds the multi-stage minimal-runtime recipe for a
// bounded-cli source tree. binary is the declared guest path the spec records
// as Source.Command (for node scripts it becomes `node <binary>`).
func GenerateCLIRecipe(contextBytes []byte, language, baseImage, binary string) (ArtifactRecipe, []byte, error) {
	if !validGuestBinaryPath(binary) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: cli binary guest path required", ErrInvalid)
	}
	files, err := readArtifactContext(contextBytes)
	if err != nil {
		return ArtifactRecipe{}, nil, err
	}
	language, err = detectRecipeLanguage(files, language)
	if err != nil {
		return ArtifactRecipe{}, nil, err
	}
	manifest, supported := languageManifests[language]
	if !supported {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: supported language required", ErrInvalid)
	}
	if baseImage == "" {
		baseImage = languageBaseImages[language]
	}
	if files[manifest] == nil && !(language == "python" && files["requirements.txt"] != nil) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: language manifest missing", ErrInvalid)
	}
	runPrefix := buildCacheRunPrefix(language)
	build := []string{"FROM " + baseImage + " AS build", "ARG HTTP_PROXY", "ARG HTTPS_PROXY", "ARG NO_PROXY", "WORKDIR /app", "COPY . /app"}
	locks := []string{manifest}
	var runtime []string
	entrypoint := []string{binary}
	switch language {
	case "go":
		mains := goMainDirs(files)
		if len(mains) != 1 {
			mains = disambiguateGoMain(mains, files)
		}
		if len(mains) != 1 {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Go recipe requires one main package", ErrInvalid)
		}
		build = append(build, "ENV CGO_ENABLED=0")
		command, _ := json.Marshal([]string{"go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-o", "/app/tool", mains[0]})
		build = append(build, "RUN "+runPrefix+string(command))
		if files["go.sum"] != nil {
			locks = append(locks, "go.sum")
		}
		runtime = []string{
			"FROM scratch",
			"COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt",
			"COPY --from=build /app/tool " + binary,
		}
	case "rust":
		if files["Cargo.lock"] == nil {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Rust recipe requires Cargo.lock", ErrInvalid)
		}
		locks = []string{"Cargo.toml", "Cargo.lock"}
		name := artifactTOMLSection(files[manifest], "package")["name"]
		if !toolNamePattern.MatchString(name) || files["src/main.rs"] == nil || strings.Contains(string(files[manifest]), "[[bin]]") || strings.Contains(string(files[manifest]), "[workspace]") {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Rust recipe requires one package binary", ErrInvalid)
		}
		// musl gives a static binary so the runtime stage stays scratch.
		// static.rust-lang.org is inside the restricted build egress set.
		build = append(build,
			"RUN "+runPrefix+"rustup target add x86_64-unknown-linux-musl",
			"RUN "+runPrefix+"cargo build --release --locked --target x86_64-unknown-linux-musl")
		runtime = []string{
			"FROM scratch",
			"COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt",
			"COPY --from=build /app/target/x86_64-unknown-linux-musl/release/" + name + " " + binary,
		}
	case "python":
		if !strings.HasPrefix(binary, "/opt/venv/bin/") {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: python cli binary must live under /opt/venv/bin", ErrInvalid)
		}
		if files["requirements.txt"] != nil {
			build = append(build, "RUN "+runPrefix+"python -m venv /opt/venv", "RUN "+runPrefix+"/opt/venv/bin/pip install -r requirements.txt")
			locks = []string{"requirements.txt"}
		}
		if files["pyproject.toml"] != nil {
			build = append(build, "RUN "+runPrefix+"python -m venv /opt/venv", "RUN "+runPrefix+"/opt/venv/bin/pip install .")
		}
		// `test -x` fails the build closed when the declared console script is
		// absent — the spec cannot smuggle a path the image never produced.
		runtime = []string{
			"FROM " + baseImage,
			"COPY --from=build /opt/venv /opt/venv",
			"RUN test -x " + binary,
		}
	case "node":
		pkg := struct {
			Bin     json.RawMessage
			Scripts map[string]string
		}{}
		if json.Unmarshal(files[manifest], &pkg) != nil {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: malformed package.json", ErrInvalid)
		}
		pnpm := files["pnpm-lock.yaml"] != nil
		if files["package-lock.json"] == nil && !pnpm {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Node recipe requires package-lock.json", ErrInvalid)
		}
		locks = []string{"package-lock.json"}
		if pnpm {
			locks = []string{"pnpm-lock.yaml"}
			build = append(build, "RUN "+runPrefix+"npm install --global pnpm@10.32.1", "RUN "+runPrefix+"pnpm install --frozen-lockfile")
		} else {
			build = append(build, "RUN "+runPrefix+"npm ci")
		}
		if pkg.Scripts["build"] != "" {
			build = append(build, "RUN "+runPrefix+"npm run build")
		}
		switch {
		case cliBinaryScriptPattern.MatchString(binary) && strings.HasPrefix(binary, "/app/"):
			// Command stays an absolute guest path: the definition pins the
			// interpreter inside the pinned image, never a PATH lookup.
			entrypoint = []string{"/usr/local/bin/node", binary}
		case strings.HasPrefix(binary, "/app/node_modules/.bin/"):
			// npm bin shim: executable script with a node shebang.
		default:
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: node cli binary must be a /app script or node_modules/.bin shim", ErrInvalid)
		}
		runtime = []string{
			"FROM " + baseImage,
			"COPY --from=build /app /app",
			"WORKDIR /app",
			"RUN test -e " + binary,
		}
	default:
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: supported language required", ErrInvalid)
	}
	argv, _ := json.Marshal(entrypoint)
	lines := append(append(build, runtime...), "USER 10001:10001", "CMD []", "ENTRYPOINT "+string(argv))
	const dockerfile = ".hub/Dockerfile"
	if files[dockerfile] != nil {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: reserved generated recipe path", ErrInvalid)
	}
	files[dockerfile] = []byte(strings.Join(lines, "\n") + "\n")
	recipe := ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: dockerfile, DependencyLocks: locks, BaseImages: []string{baseImage}, Entrypoint: entrypoint}
	output, err := writeRecipeContext(&recipe, files)
	if err != nil {
		return ArtifactRecipe{}, nil, err
	}
	return recipe, output, recipe.VerifyContext(output)
}

// contextHasDockerfile reports whether the verified context carries an
// upstream Dockerfile. Present Dockerfiles keep the standard single-stage
// recipe path; absent ones route to cli mode.
func contextHasDockerfile(contextBytes []byte) bool {
	reader := tar.NewReader(bytes.NewReader(contextBytes))
	for {
		h, err := reader.Next()
		if err != nil {
			return false
		}
		if h.Typeflag == tar.TypeReg && h.Name == "Dockerfile" {
			return true
		}
	}
}
