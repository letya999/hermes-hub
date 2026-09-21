package toolhub

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

type ArtifactFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// ArtifactRecipe is one language-neutral, explicitly supplied Dockerfile recipe.
// No recipe command is ever run on the host. Validated metadata is not a trusted
// artifact: isolated build, scans, discovery and human review remain mandatory.
type ArtifactRecipe struct {
	Format          string         `json:"format"`
	Dockerfile      string         `json:"dockerfile"`
	Files           []ArtifactFile `json:"files"`
	DependencyLocks []string       `json:"dependency_locks"`
	BaseImages      []string       `json:"base_images"`
	Entrypoint      []string       `json:"entrypoint"`
}

func ParseArtifactRecipe(data []byte) (ArtifactRecipe, error) {
	var recipe ArtifactRecipe
	if len(data) > 1<<20 {
		return recipe, fmt.Errorf("%w: oversized recipe", ErrInvalid)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&recipe) != nil || d.Decode(new(any)) != io.EOF {
		return ArtifactRecipe{}, fmt.Errorf("%w: one explicit recipe JSON document required", ErrInvalid)
	}
	return recipe, recipe.Validate()
}

func (r ArtifactRecipe) Validate() error {
	if r.Format != "dockerfile-v1" || !artifactContextPath(r.Dockerfile) || len(r.Files) == 0 || len(r.Files) > 4096 || len(r.DependencyLocks) == 0 || len(r.DependencyLocks) > 32 || len(r.BaseImages) == 0 || len(r.BaseImages) > 32 || len(r.Entrypoint) == 0 || len(r.Entrypoint) > 32 || !validCommand(r.Entrypoint[0]) {
		return fmt.Errorf("%w: supported Dockerfile recipe, entrypoint, locks and pinned bases required", ErrInvalid)
	}
	files := map[string]bool{}
	for _, file := range r.Files {
		if !artifactContextPath(file.Path) || !digestPattern.MatchString(file.Digest) || files[file.Path] {
			return fmt.Errorf("%w: unsafe, unpinned or duplicate recipe file", ErrInvalid)
		}
		files[file.Path] = true
	}
	if !files[r.Dockerfile] {
		return fmt.Errorf("%w: Dockerfile must be in pinned context", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, name := range r.DependencyLocks {
		if !files[name] || seen[name] {
			return fmt.Errorf("%w: pinned dependency lock file required", ErrInvalid)
		}
		seen[name] = true
	}
	seen = map[string]bool{}
	for _, image := range r.BaseImages {
		name, digest, ok := strings.Cut(image, "@")
		if !ok || name == "" || !commandPattern.MatchString(strings.ReplaceAll(name, ":", "/")) || strings.Contains(name, "..") || !digestPattern.MatchString(digest) || seen[image] {
			return fmt.Errorf("%w: immutable base image digest required", ErrInvalid)
		}
		seen[image] = true
	}
	for _, arg := range r.Entrypoint {
		if len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") || strings.Contains(arg, "${") {
			return fmt.Errorf("%w: bounded literal entrypoint argv required", ErrInvalid)
		}
	}
	return nil
}

// VerifyContext rejects any unlisted file or drift from the reviewed file bytes.
// A pinned lock file is not proof that its dependencies are pinned; a scanner
// must inspect its semantics before trust can be granted.
func (r ArtifactRecipe) VerifyContext(contextBytes []byte) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if len(contextBytes) > 72<<20 {
		return fmt.Errorf("%w: oversized context", ErrInvalid)
	}
	expected := map[string]string{}
	for _, file := range r.Files {
		expected[file.Path] = file.Digest
	}
	input := bytes.NewReader(contextBytes)
	reader := tar.NewReader(input)
	remaining := int64(64 << 20)
	var dockerfile []byte
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: malformed context", ErrInvalid)
		}
		digest, ok := expected[h.Name]
		if !ok || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > remaining || h.Mode != 0644 || h.Uid != 0 || h.Gid != 0 || h.Linkname != "" {
			return fmt.Errorf("%w: unexpected or oversized context file", ErrInvalid)
		}
		for key, value := range h.PAXRecords {
			if key != "path" || value != h.Name {
				return fmt.Errorf("%w: unreviewed context metadata", ErrInvalid)
			}
		}
		remaining -= h.Size
		if h.Name == r.Dockerfile && h.Size > 65536 {
			return fmt.Errorf("%w: oversized Dockerfile", ErrInvalid)
		}
		hash := sha256.New()
		var dst io.Writer = hash
		var content bytes.Buffer
		if h.Name == r.Dockerfile {
			dst = io.MultiWriter(hash, &content)
		}
		if _, err := io.Copy(dst, reader); err != nil {
			return fmt.Errorf("%w: truncated context", ErrInvalid)
		}
		if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
			return fmt.Errorf("%w: recipe context digest drift", ErrInvalid)
		}
		if h.Name == r.Dockerfile {
			dockerfile = content.Bytes()
		}
		delete(expected, h.Name)
	}
	if len(expected) != 0 || input.Len() != 0 {
		return fmt.Errorf("%w: recipe context files missing", ErrInvalid)
	}
	return r.verifyDockerfile(dockerfile)
}

// ponytail: dockerfile-v1 has a conservative single-line grammar; use the pinned
// BuildKit parser if supporting richer Dockerfiles becomes necessary.
// Complex upstream Dockerfiles need an explicitly prepared recipe, not guesses.
func (r ArtifactRecipe) verifyDockerfile(data []byte) error {
	stages := map[string]bool{}
	var entrypoint []string
	user := ""
	from := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if strings.Contains(line, "=") {
				return fmt.Errorf("%w: custom Dockerfile parser directives unsupported", ErrInvalid)
			}
			continue
		}
		if strings.ContainsAny(line, "\x00\\`") || strings.Contains(line, "<<") {
			return fmt.Errorf("%w: multiline Dockerfile recipe unsupported", ErrInvalid)
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("%w: malformed Dockerfile instruction", ErrInvalid)
		}
		instruction := strings.ToUpper(fields[0])
		if instruction != "FROM" && !from {
			return fmt.Errorf("%w: Dockerfile must begin with FROM", ErrInvalid)
		}
		switch instruction {
		case "ARG":
			// Only BuildKit's three predefined proxy args are allowed. They are
			// supplied by the isolated builder and never become image ENV.
			if len(fields) != 2 || (fields[1] != "HTTP_PROXY" && fields[1] != "HTTPS_PROXY" && fields[1] != "NO_PROXY") {
				return fmt.Errorf("%w: only proxy ARGs are supported", ErrInvalid)
			}
		case "FROM":
			if (len(fields) != 2 && (len(fields) != 4 || strings.ToUpper(fields[2]) != "AS" || !toolNamePattern.MatchString(fields[3]) || stages[fields[3]])) || (!slices.Contains(r.BaseImages, fields[1]) && !stages[fields[1]] && fields[1] != "scratch") {
				return fmt.Errorf("%w: undeclared or mutable Dockerfile base", ErrInvalid)
			}
			if len(fields) == 4 {
				stages[fields[3]] = true
			}
			from, entrypoint, user = true, nil, ""
		case "ENTRYPOINT":
			if json.Unmarshal([]byte(strings.TrimSpace(line[len(fields[0]):])), &entrypoint) != nil {
				return fmt.Errorf("%w: exec-form entrypoint required", ErrInvalid)
			}
		case "USER":
			user = strings.Join(fields[1:], " ")
		case "COPY", "RUN", "WORKDIR", "ENV", "LABEL", "CMD", "EXPOSE":
			rest := fields[1:]
			for instruction == "RUN" && len(rest) > 0 && strings.HasPrefix(rest[0], "--mount=") {
				if err := validateCacheMountFlag(rest[0]); err != nil {
					return err
				}
				rest = rest[1:]
			}
			if len(rest) == 0 || (strings.HasPrefix(rest[0], "--") && instruction != "COPY") {
				return fmt.Errorf("%w: build entitlements and mounts unsupported", ErrInvalid)
			}
			if instruction == "COPY" {
				for _, field := range fields[1:] {
					if strings.HasPrefix(field, "--") && (!strings.HasPrefix(field, "--from=") || !stages[strings.TrimPrefix(field, "--from=")]) {
						return fmt.Errorf("%w: unsupported COPY option or external source", ErrInvalid)
					}
				}
			}
		default:
			return fmt.Errorf("%w: unsupported Dockerfile instruction %s", ErrInvalid, instruction)
		}
	}
	if !from || user != "10001:10001" || !slices.Equal(entrypoint, r.Entrypoint) {
		return fmt.Errorf("%w: final-stage non-root owner and declared entrypoint required", ErrInvalid)
	}
	return nil
}

var (
	cacheMountIDPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	cacheMountModePattern  = regexp.MustCompile(`^0?[0-7]{3,4}$`)
	cacheMountOwnerPattern = regexp.MustCompile(`^[0-9]{1,7}$`)
)

// validateCacheMountFlag admits only BuildKit cache mounts. Cache mounts persist
// package-manager state across isolated builds; bind, secret, ssh and tmpfs
// mounts could smuggle host data or credentials into a build and stay rejected.
func validateCacheMountFlag(flag string) error {
	keys := map[string]bool{}
	for _, pair := range strings.Split(strings.TrimPrefix(flag, "--mount="), ",") {
		key, value, found := strings.Cut(pair, "=")
		if !found && key == "readonly" {
			value = "true"
		}
		if !found && key != "readonly" || keys[key] {
			return fmt.Errorf("%w: malformed cache build mount", ErrInvalid)
		}
		keys[key] = true
		switch key {
		case "type":
			if value != "cache" {
				return fmt.Errorf("%w: only cache build mounts are supported", ErrInvalid)
			}
		case "id":
			if !cacheMountIDPattern.MatchString(value) {
				return fmt.Errorf("%w: invalid cache mount id", ErrInvalid)
			}
		case "target":
			if !strings.HasPrefix(value, "/") || strings.Contains(value, "..") {
				return fmt.Errorf("%w: invalid cache mount target", ErrInvalid)
			}
		case "sharing":
			if value != "shared" && value != "private" && value != "locked" {
				return fmt.Errorf("%w: invalid cache mount sharing", ErrInvalid)
			}
		case "mode":
			if !cacheMountModePattern.MatchString(value) {
				return fmt.Errorf("%w: invalid cache mount mode", ErrInvalid)
			}
		case "uid", "gid":
			if !cacheMountOwnerPattern.MatchString(value) {
				return fmt.Errorf("%w: invalid cache mount owner", ErrInvalid)
			}
		case "readonly":
			if value != "true" && value != "false" {
				return fmt.Errorf("%w: invalid cache mount readonly", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: unsupported cache mount key %q", ErrInvalid, key)
		}
	}
	if !keys["type"] || !keys["target"] {
		return fmt.Errorf("%w: cache build mount requires type and target", ErrInvalid)
	}
	return nil
}
