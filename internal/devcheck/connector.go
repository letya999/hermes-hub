package devcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// connectorPin is the verified build evidence connector-build emits and
// connector-pin consumes: the archive's own OCI digests, never a re-parsed
// registry claim. Digest is the image manifest digest; provenance and SBOM
// are the attestation manifest digests inside the same archive.
type connectorPin struct {
	ID         string `json:"id"`
	Ref        string `json:"ref"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	Provenance string `json:"provenance"`
	SBOM       string `json:"sbom"`
}

var connectorTagPattern = regexp.MustCompile(`^src-[0-9a-f]{7,64}$`)

func writeConnectorPin(path string, pin connectorPin) error {
	data, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func readConnectorPin(path string) (connectorPin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return connectorPin{}, err
	}
	var pin connectorPin
	if err := json.Unmarshal(data, &pin); err != nil {
		return connectorPin{}, err
	}
	if !api.DigestPattern.MatchString(pin.Digest) || !api.DigestPattern.MatchString(pin.Provenance) || !api.DigestPattern.MatchString(pin.SBOM) || !connectorTagPattern.MatchString(pin.Tag) {
		return connectorPin{}, fmt.Errorf("pin.json carries malformed evidence")
	}
	return pin, nil
}

func connectorNamespace(repo string) string {
	return registryHost + "/" + repo + "/hermes-hub-connectors"
}

func connectorEntry(id string) (api.PreparedEntry, error) {
	entries, err := api.PreparedCatalog()
	if err != nil {
		return api.PreparedEntry{}, err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return api.PreparedEntry{}, fmt.Errorf("prepared entry %q not in catalog", id)
}

// ConnectorPublishRef resolves the canonical push ref for a catalog entry:
// ghcr.io/<owner/repo>/hermes-hub-connectors/<id>:src-<commit>.
func ConnectorPublishRef(ctx context.Context, id string) (string, error) {
	repo, err := registryRepo(ctx, cliGit)
	if err != nil {
		return "", err
	}
	entries, err := api.PreparedCatalog()
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.ID == id {
			return connectorNamespace(repo) + "/" + id + ":src-" + entry.Source.CommitSHA[:min(12, len(entry.Source.CommitSHA))], nil
		}
	}
	return "", fmt.Errorf("prepared entry %q not in catalog", id)
}

// ConnectorPin binds the catalog entry to the pushed image. The ref must be
// the pin's own ref:tag — no caller-supplied image can be pinned.
func ConnectorPin(ctx context.Context, id, pinPath, ref, catalogPath string) error {
	pin, err := readConnectorPin(pinPath)
	if err != nil {
		return err
	}
	if pin.ID != id || ref != pin.Ref+":"+pin.Tag {
		return fmt.Errorf("ref must be the pin's own %s:%s", pin.Ref, pin.Tag)
	}
	return connectorPinApply(ctx, pin, ref, catalogPath, cliDocker)
}

// ConnectorBuild compiles one embedded catalog entry through the same
// restricted source→OCI path admission uses, exports the attested archive to
// outDir/artifact.oci.tar and writes outDir/pin.json for connector-pin.
func ConnectorBuild(ctx context.Context, id, outDir string) error {
	repo, err := registryRepo(ctx, cliGit)
	if err != nil {
		return err
	}
	return connectorBuild(ctx, id, outDir, repo, api.ImportGitHubArtifact, api.OpenStoredOCIArtifact)
}

func connectorBuild(ctx context.Context, id, outDir, repo string,
	importFn func(context.Context, api.ArtifactSource, api.ArtifactImportConfig, api.RestrictedBuildConfig) (api.ImportedArtifact, error),
	openFn func(context.Context, string, string, int64) (*os.File, api.StoredOCIArtifact, error)) error {
	entry, err := connectorEntry(id)
	if err != nil {
		return err
	}
	config := api.NormalizeArtifactImportConfig(entry.Apply(api.DefaultSelfInstallConfig(entry.Source)))
	config.Image = "hermes-connector/" + id
	artifacts, err := filepath.Abs(filepath.Join(outDir, "cas"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		return err
	}
	seccomp, err := filepath.Abs("docker/seccomp-buildkit-rootless.json")
	if err != nil {
		return err
	}
	build := api.RestrictedBuildConfig{SeccompPath: seccomp, ArtifactDirectory: artifacts, MaxArtifactBytes: 8 << 30}
	packet, err := importFn(ctx, entry.Source, config, build)
	if err != nil {
		return err
	}
	// The catalog pin describes the reviewed entrypoint; a generated recipe
	// that disagrees would pin a different artifact than the entry claims.
	if strings.Join(packet.Recipe.Entrypoint, " ") != strings.Join(entry.Entrypoint, " ") {
		return fmt.Errorf("generated recipe entrypoint differs from the reviewed catalog entrypoint")
	}
	object, _, err := openFn(ctx, artifacts, packet.Artifact.ArchiveDigest, build.MaxArtifactBytes)
	if err != nil {
		return err
	}
	defer object.Close()
	target := filepath.Join(outDir, "artifact.oci.tar")
	temporary := target + ".partial"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, object); err != nil {
		file.Close() // #nosec G104 -- error path already; close failure is secondary.
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	pin := connectorPin{
		ID:         id,
		Ref:        connectorNamespace(repo) + "/" + id,
		Tag:        "src-" + entry.Source.CommitSHA[:min(12, len(entry.Source.CommitSHA))],
		Digest:     packet.Artifact.Evidence.ImageManifestDigest,
		Provenance: packet.Artifact.Evidence.ProvenanceDigest,
		SBOM:       packet.Artifact.Evidence.SBOMDigest,
	}
	return writeConnectorPin(filepath.Join(outDir, "pin.json"), pin)
}

// connectorCatalogPatch rewrites exactly one entry's tier and image pin. The
// catalog file keeps its reviewed inline style, so this is a surgical line
// edit inside the entry's braces rather than a full re-encode.
func connectorCatalogPatch(data []byte, pin connectorPin) ([]byte, error) {
	imageLine := `    "image": {"ref": "` + pin.Ref + "@" + pin.Digest + `", "digest": "` + pin.Digest + `", "provenance": "` + pin.Provenance + `", "sbom": "` + pin.SBOM + `"},`
	lines := strings.Split(string(data), "\n")
	// Entries are flat objects; the id field opens each entry's first line at
	// four-space indent. Find the entry, then its closing brace line.
	start, end := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, `    "id": "`+pin.ID+`"`) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "  }") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		return nil, fmt.Errorf("catalog entry %q not found", pin.ID)
	}
	tierAt, imageAt := -1, -1
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, `"tier":`) {
			tierAt = i
		}
		if strings.HasPrefix(trimmed, `"image":`) {
			imageAt = i
		}
	}
	if tierAt < 0 {
		return nil, fmt.Errorf("catalog entry %q has no tier line to anchor the pin", pin.ID)
	}
	lines[tierAt] = `    "tier": "t0",`
	if imageAt >= 0 {
		lines[imageAt] = imageLine
	} else {
		lines = append(lines[:tierAt+1:tierAt+1], append([]string{imageLine}, lines[tierAt+1:]...)...)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// connectorPinApply verifies the pushed ref's manifest digest against the
// locally verified archive digest, then promotes the catalog entry. The
// manifest bytes come from `skopeo inspect --raw`; hashing them reproduces
// the registry digest without trusting the tool's metadata.
func connectorPinApply(ctx context.Context, pin connectorPin, ref, catalogPath string, run func(context.Context, ...string) ([]byte, error)) error {
	raw, err := run(ctx, "skopeo", "inspect", "--raw", "docker://"+ref)
	if err != nil {
		return fmt.Errorf("registry manifest fetch failed: %w", err)
	}
	sum := sha256.Sum256(raw)
	if digest := "sha256:" + hex.EncodeToString(sum[:]); digest != pin.Digest {
		return fmt.Errorf("pushed manifest digest %s != verified archive digest %s", digest, pin.Digest)
	}
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		return err
	}
	patched, err := connectorCatalogPatch(data, pin)
	if err != nil {
		return err
	}
	// The patch is valid only if the whole catalog still round-trips through
	// the strict parser; a corrupt splice fails here, never at install time.
	if _, err := api.ParsePreparedCatalog(patched); err != nil {
		return fmt.Errorf("patched catalog invalid: %w", err)
	}
	return os.WriteFile(catalogPath, patched, 0o600) // #nosec G703 -- catalog path is the operator's CLI argument.
}
