package toolhub

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"path"
	"slices"
	"strings"
)

// GenerateArtifactRecipe supplies our Dockerfile, not an upstream Dockerfile.
// An optional literal entrypoint resolves ambiguous servers; no host build runs.
// The base image is a trusted pinned toolchain selection, never source advice.
func GenerateArtifactRecipe(contextBytes []byte, language, baseImage string, entrypoint []string) (ArtifactRecipe, []byte, error) {
	if len(contextBytes) > 72<<20 {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: oversized context", ErrInvalid)
	}
	files := map[string][]byte{}
	input := bytes.NewReader(contextBytes)
	reader := tar.NewReader(input)
	remaining := int64(64 << 20)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !artifactContextPath(h.Name) || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > remaining || len(files) >= 4096 {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: unsafe language context", ErrInvalid)
		}
		if _, duplicate := files[h.Name]; duplicate {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: duplicate context", ErrInvalid)
		}
		remaining -= h.Size
		data, err := io.ReadAll(reader)
		if err != nil {
			return ArtifactRecipe{}, nil, err
		}
		files[h.Name] = data
	}
	if input.Len() != 0 {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: trailing context data", ErrInvalid)
	}
	manifests := map[string]string{"python": "pyproject.toml", "node": "package.json", "go": "go.mod", "rust": "Cargo.toml"}
	if language == "" {
		for name, manifest := range manifests {
			if _, exists := files[manifest]; exists {
				if language != "" {
					return ArtifactRecipe{}, nil, fmt.Errorf("%w: multiple languages; select one", ErrInvalid)
				}
				language = name
			}
		}
		if language == "" && files["requirements.txt"] != nil {
			language = "python"
		}
	}
	manifest, supported := manifests[language]
	if !supported {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: supported language required", ErrInvalid)
	}
	if baseImage == "" {
		baseImage = map[string]string{
			"python": "python@sha256:09f7da3bc104798d0afb40bc08d23ab2da20a76130cec1f2ef170848f5d85217",
			"node":   "node@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae",
			"go":     "golang@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b",
			"rust":   "rust@sha256:ebd900bae66fd508b466cef82d64a83a5fb34682e4c8b2797a42908bddc95a57",
		}[language]
	}
	if files[manifest] == nil && !(language == "python" && files["requirements.txt"] != nil) {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: language manifest missing", ErrInvalid)
	}
	// BuildKit only exposes proxy build arguments inside a stage after they are
	// declared.  Keep these as ARGs (never ENV) so the proxy endpoint cannot
	// persist in the resulting image config or runtime environment.
	lines := []string{"FROM " + baseImage, "ARG HTTP_PROXY", "ARG HTTPS_PROXY", "ARG NO_PROXY", "WORKDIR /app", "COPY . /app"}
	locks := []string{manifest}
	var automatic [][]string
	switch language {
	case "node":
		var pkg struct {
			Name    string
			Bin     json.RawMessage
			Scripts map[string]string
		}
		if json.Unmarshal(files[manifest], &pkg) != nil {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: malformed package.json", ErrInvalid)
		}
		pnpm := files["pnpm-lock.yaml"] != nil
		if files["package-lock.json"] == nil && !pnpm {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Node recipe requires package-lock.json", ErrInvalid)
		}
		locks = []string{"package-lock.json"}
		manager := "npm"
		if pnpm {
			manager = "pnpm"
			locks = []string{"pnpm-lock.yaml"}
			lines = append(lines, "RUN npm install --global pnpm@10.32.1", "RUN pnpm install --frozen-lockfile")
		} else {
			lines = append(lines, "RUN npm ci")
		}
		if pkg.Scripts["build"] != "" {
			lines = append(lines, "RUN "+manager+" run build")
		}
		binDirectory := "/app/"
		if pnpm && len(pkg.Bin) == 0 {
			// ponytail: conventional *-mcp workspace discovery; explicit argv for
			// other layouts instead of inferring arbitrary package scripts.
			selected := ""
			rootBuild := pkg.Scripts["build"] != ""
			for name, data := range files {
				if !strings.HasSuffix(name, "/package.json") {
					continue
				}
				candidate := pkg
				candidate.Name, candidate.Bin, candidate.Scripts = "", nil, nil
				if json.Unmarshal(data, &candidate) != nil || !strings.HasSuffix(candidate.Name, "-mcp") || len(candidate.Bin) == 0 {
					continue
				}
				if selected != "" {
					return ArtifactRecipe{}, nil, fmt.Errorf("%w: ambiguous MCP workspace", ErrInvalid)
				}
				selected = name
				pkg = candidate
			}
			if selected != "" {
				binDirectory += path.Dir(selected) + "/"
				if !rootBuild && pkg.Scripts["build"] != "" {
					command, _ := json.Marshal([]string{"pnpm", "--dir", path.Dir(selected), "run", "build"})
					lines = append(lines, "RUN "+string(command))
				}
			}
		}
		var bin string
		if json.Unmarshal(pkg.Bin, &bin) == nil && artifactContextPath(strings.TrimPrefix(bin, "./")) {
			bin = strings.TrimPrefix(bin, "./")
			automatic = append(automatic, []string{"node", binDirectory + bin})
		} else {
			var bins map[string]string
			if json.Unmarshal(pkg.Bin, &bins) == nil {
				for _, file := range bins {
					file = strings.TrimPrefix(file, "./")
					if artifactContextPath(file) {
						automatic = append(automatic, []string{"node", binDirectory + file})
					}
				}
			}
		}
		if len(automatic) == 0 {
			start := strings.Fields(pkg.Scripts["start"])
			if len(start) == 2 && start[0] == "node" && artifactContextPath(start[1]) {
				automatic = append(automatic, []string{"node", "/app/" + start[1]})
			}
		}
	case "python":
		lines = append(lines, "RUN python -m venv /opt/venv")
		if files["requirements.txt"] != nil {
			lines = append(lines, "RUN /opt/venv/bin/pip install -r requirements.txt")
			locks = []string{"requirements.txt"}
		}
		if files["pyproject.toml"] != nil {
			lines = append(lines, "RUN /opt/venv/bin/pip install .")
			scripts := artifactTOMLSection(files[manifest], "project.scripts")
			primary := artifactTOMLSection(files[manifest], "project")["name"]
			if scripts[primary] != "" {
				scripts = map[string]string{primary: scripts[primary]}
			}
			for name := range scripts {
				if validCommand(name) && !strings.Contains(name, "/") {
					automatic = append(automatic, []string{"/opt/venv/bin/" + name})
				}
			}
		}
		if len(automatic) == 0 {
			for _, file := range []string{"server.py", "main.py", "__main__.py"} {
				if files[file] != nil {
					automatic = append(automatic, []string{"/opt/venv/bin/python", "/app/" + file})
				}
			}
		}
	case "go":
		lines = append(lines, "ENV CGO_ENABLED=0")
		for name, data := range files {
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), name, data, parser.PackageClauseOnly)
			if err == nil && f.Name.Name == "main" {
				target := "./" + path.Dir(name)
				candidate := []string{target}
				if !slices.ContainsFunc(automatic, func(existing []string) bool { return slices.Equal(existing, candidate) }) {
					automatic = append(automatic, candidate)
				}
			}
		}
		if len(automatic) != 1 {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Go recipe requires one main package", ErrInvalid)
		}
		// JSON exec form avoids interpolating even repository directory names.
		command, _ := json.Marshal([]string{"go", "build", "-mod=readonly", "-buildvcs=false", "-trimpath", "-o", "/app/mcp-server", automatic[0][0]})
		lines = append(lines, "RUN "+string(command))
		automatic = [][]string{{"/app/mcp-server"}}
		if files["go.sum"] != nil {
			locks = append(locks, "go.sum")
		}
	case "rust":
		if files["Cargo.lock"] == nil {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Rust recipe requires Cargo.lock", ErrInvalid)
		}
		locks = []string{"Cargo.lock"}
		name := artifactTOMLSection(files[manifest], "package")["name"]
		if !toolNamePattern.MatchString(name) || files["src/main.rs"] == nil || strings.Contains(string(files[manifest]), "[[bin]]") || strings.Contains(string(files[manifest]), "[workspace]") {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: Rust recipe requires one package binary", ErrInvalid)
		}
		lines = append(lines, "RUN cargo build --release --locked")
		automatic = [][]string{{"/app/target/release/" + name}}
	}
	if len(entrypoint) == 0 {
		if len(automatic) != 1 {
			return ArtifactRecipe{}, nil, fmt.Errorf("%w: ambiguous MCP entrypoint; select literal argv", ErrInvalid)
		}
		entrypoint = automatic[0]
	}
	argv, _ := json.Marshal(entrypoint)
	lines = append(lines, "USER 10001:10001", "CMD []", "ENTRYPOINT "+string(argv))
	const dockerfile = ".hub/Dockerfile"
	if files[dockerfile] != nil {
		return ArtifactRecipe{}, nil, fmt.Errorf("%w: reserved generated recipe path", ErrInvalid)
	}
	files[dockerfile] = []byte(strings.Join(lines, "\n") + "\n")
	recipe := ArtifactRecipe{Format: "dockerfile-v1", Dockerfile: dockerfile, DependencyLocks: locks, BaseImages: []string{baseImage}, Entrypoint: entrypoint}
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		data := files[name]
		digest := sha256.Sum256(data)
		recipe.Files = append(recipe.Files, ArtifactFile{Path: name, Digest: "sha256:" + hex.EncodeToString(digest[:])})
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(data))}); err != nil {
			return ArtifactRecipe{}, nil, err
		}
		if _, err := writer.Write(data); err != nil {
			return ArtifactRecipe{}, nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return ArtifactRecipe{}, nil, err
	}
	return recipe, output.Bytes(), recipe.VerifyContext(output.Bytes())
}

// ponytail: only simple quoted TOML keys in relevant tables. A full TOML parser
// is needed when richer manifests must be automatically disambiguated.
func artifactTOMLSection(data []byte, wanted string) map[string]string {
	result := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
			continue
		}
		if section != wanted {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		value = strings.TrimSpace(value)
		if ok && len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			result[strings.Trim(strings.TrimSpace(key), "\"'")] = value[1 : len(value)-1]
		}
	}
	return result
}
