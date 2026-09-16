package toolhub

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const ociIndexType = "application/vnd.oci.image.index.v1+json"
const ociManifestType = "application/vnd.oci.image.manifest.v1+json"

// OCIArtifactEvidence distinguishes archive/index, runnable manifest and image
// config digests. Attestation presence/integrity is not proof of an honest build,
// complete SBOM or approved source: isolated builder receipts and review remain
// mandatory before admission. This verifier never extracts image layers.
type OCIArtifactEvidence struct {
	LayoutDigest        string `json:"layout_digest"`
	IndexDigest         string `json:"index_digest"`
	ImageManifestDigest string `json:"image_manifest_digest"`
	ImageConfigDigest   string `json:"image_config_digest"`
	ProvenanceDigest    string `json:"provenance_digest"`
	SBOMDigest          string `json:"sbom_digest"`
	Architecture        string `json:"architecture"`
}

type ociDescriptor struct {
	MediaType, Digest string
	Size              int64
	Data              []byte
	Annotations       map[string]string
	Platform          struct{ OS, Architecture string }
}

type ociDocument struct {
	SchemaVersion           int
	MediaType, ArtifactType string
	Manifests, Layers       []ociDescriptor
	Config                  ociDescriptor
	Subject                 *ociDescriptor
}

func VerifyOCIArtifact(archive io.ReaderAt, size, maxBytes int64) (OCIArtifactEvidence, error) {
	var evidence OCIArtifactEvidence
	invalid := func() (OCIArtifactEvidence, error) {
		return OCIArtifactEvidence{}, fmt.Errorf("%w: invalid OCI artifact evidence", ErrInvalid)
	}
	if archive == nil || size < 1 || size > maxBytes || maxBytes < 1 || maxBytes > 8<<30 {
		return invalid()
	}
	type location struct {
		offset, size int64
		digest       string
	}
	files := map[string]location{}
	section := io.NewSectionReader(archive, 0, size)
	r := tar.NewReader(section)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return invalid()
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir && (strings.TrimSuffix(name, "/") == "blobs" || strings.TrimSuffix(name, "/") == "blobs/sha256") {
			continue
		}
		_, duplicate := files[name]
		isBlob := strings.HasPrefix(name, "blobs/sha256/") && digestPattern.MatchString("sha256:"+strings.TrimPrefix(name, "blobs/sha256/"))
		if duplicate || len(files) >= 4096 || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxBytes || (!isBlob && name != "index.json" && name != "oci-layout") || h.Linkname != "" {
			return invalid()
		}
		position, err := section.Seek(0, io.SeekCurrent)
		if err != nil {
			return invalid()
		}
		hash := sha256.New()
		n, err := io.Copy(hash, r)
		if err != nil || n != h.Size {
			return invalid()
		}
		digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
		if isBlob && digest != "sha256:"+strings.TrimPrefix(name, "blobs/sha256/") {
			return invalid()
		}
		files[name] = location{position, h.Size, digest}
	}
	position, err := section.Seek(0, io.SeekCurrent)
	if err != nil || position != size {
		return invalid()
	}
	read := func(name string, expectedSize int64, digest string, value any) error {
		loc, ok := files[name]
		if !ok || loc.size > 16<<20 || (expectedSize >= 0 && loc.size != expectedSize) || (digest != "" && loc.digest != digest) {
			return ErrInvalid
		}
		data, err := io.ReadAll(io.NewSectionReader(archive, loc.offset, loc.size))
		if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(data)) != loc.digest || json.Unmarshal(data, value) != nil {
			return ErrInvalid
		}
		return nil
	}
	readDescriptor := func(d ociDescriptor, value any) error {
		if !digestPattern.MatchString(d.Digest) || d.Size < 0 {
			return ErrInvalid
		}
		if d.Data != nil {
			if len(d.Data) > 16<<20 || int64(len(d.Data)) != d.Size || fmt.Sprintf("sha256:%x", sha256.Sum256(d.Data)) != d.Digest || json.Unmarshal(d.Data, value) != nil {
				return ErrInvalid
			}
			return nil
		}
		return read("blobs/sha256/"+strings.TrimPrefix(d.Digest, "sha256:"), d.Size, d.Digest, value)
	}
	checkDescriptor := func(d ociDescriptor) bool {
		if d.Data != nil {
			return len(d.Data) <= 16<<20 && int64(len(d.Data)) == d.Size && fmt.Sprintf("sha256:%x", sha256.Sum256(d.Data)) == d.Digest
		}
		loc, ok := files["blobs/sha256/"+strings.TrimPrefix(d.Digest, "sha256:")]
		return digestPattern.MatchString(d.Digest) && ok && loc.size == d.Size && loc.digest == d.Digest
	}
	var layout struct{ ImageLayoutVersion string }
	var index ociDocument
	if read("oci-layout", -1, "", &layout) != nil || layout.ImageLayoutVersion != "1.0.0" || read("index.json", -1, "", &index) != nil {
		return invalid()
	}
	evidence.LayoutDigest = files["index.json"].digest
	evidence.IndexDigest = evidence.LayoutDigest
	if index.SchemaVersion != 2 || (index.MediaType != "" && index.MediaType != ociIndexType) {
		return invalid()
	}
	// Exporters may wrap the content index in a named layout reference.
	if len(index.Manifests) == 1 && index.Manifests[0].MediaType == ociIndexType {
		d := index.Manifests[0]
		if readDescriptor(d, &index) != nil {
			return invalid()
		}
		evidence.IndexDigest = d.Digest
	}
	if index.SchemaVersion != 2 || (index.MediaType != "" && index.MediaType != ociIndexType) || len(index.Manifests) < 2 || len(index.Manifests) > 32 {
		return invalid()
	}
	attestations := []struct {
		descriptor ociDescriptor
		manifest   ociDocument
	}{}
	for _, d := range index.Manifests {
		var manifest ociDocument
		if d.MediaType != ociManifestType || readDescriptor(d, &manifest) != nil || manifest.SchemaVersion != 2 || manifest.MediaType != ociManifestType || !checkDescriptor(manifest.Config) || len(manifest.Layers) > 256 {
			return invalid()
		}
		for _, layer := range manifest.Layers {
			if !checkDescriptor(layer) {
				return invalid()
			}
		}
		if d.Annotations["vnd.docker.reference.type"] == "attestation-manifest" || manifest.ArtifactType == "application/vnd.docker.attestation.manifest.v1+json" {
			expectedConfigType := "application/vnd.oci.image.config.v1+json"
			if manifest.ArtifactType == "application/vnd.docker.attestation.manifest.v1+json" {
				expectedConfigType = "application/vnd.oci.empty.v1+json"
				if manifest.Subject == nil {
					return invalid()
				}
			} else if manifest.ArtifactType != "" {
				return invalid()
			}
			var ignoredConfig map[string]json.RawMessage
			if manifest.Config.MediaType != expectedConfigType || readDescriptor(manifest.Config, &ignoredConfig) != nil || ignoredConfig == nil {
				return invalid()
			}
			if d.Platform.OS != "unknown" || d.Platform.Architecture != "unknown" {
				return invalid()
			}
			attestations = append(attestations, struct {
				descriptor ociDescriptor
				manifest   ociDocument
			}{d, manifest})
			continue
		}
		if evidence.ImageManifestDigest != "" || d.Platform.OS != "linux" || (d.Platform.Architecture != "amd64" && d.Platform.Architecture != "arm64") || manifest.ArtifactType != "" || manifest.Config.MediaType != "application/vnd.oci.image.config.v1+json" {
			return invalid()
		}
		evidence.ImageManifestDigest, evidence.ImageConfigDigest, evidence.Architecture = d.Digest, manifest.Config.Digest, d.Platform.Architecture
		var config struct {
			OS, Architecture string
			RootFS           struct {
				Type    string
				DiffIDs []string `json:"diff_ids"`
			}
		}
		if readDescriptor(manifest.Config, &config) != nil || config.OS != "linux" || config.Architecture != d.Platform.Architecture || config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(manifest.Layers) {
			return invalid()
		}
		for _, digest := range config.RootFS.DiffIDs {
			if !digestPattern.MatchString(digest) {
				return invalid()
			}
		}
		for _, layer := range manifest.Layers {
			if layer.MediaType != "application/vnd.oci.image.layer.v1.tar" && layer.MediaType != "application/vnd.oci.image.layer.v1.tar+gzip" && layer.MediaType != "application/vnd.oci.image.layer.v1.tar+zstd" {
				return invalid()
			}
		}
	}
	if evidence.ImageManifestDigest == "" {
		return invalid()
	}
	for _, attestation := range attestations {
		reference := attestation.descriptor.Annotations["vnd.docker.reference.digest"]
		if attestation.manifest.Subject != nil {
			subject := *attestation.manifest.Subject
			if !checkDescriptor(subject) || subject.MediaType != ociManifestType || (reference != "" && reference != subject.Digest) {
				return invalid()
			}
			reference = subject.Digest
		}
		if reference != evidence.ImageManifestDigest {
			return invalid()
		}
		for _, layer := range attestation.manifest.Layers {
			var statement struct {
				Type          string `json:"_type"`
				PredicateType string
				Subject       []struct{ Digest map[string]string }
				Predicate     map[string]json.RawMessage
			}
			if layer.MediaType != "application/vnd.in-toto+json" || readDescriptor(layer, &statement) != nil || (statement.Type != "https://in-toto.io/Statement/v0.1" && statement.Type != "https://in-toto.io/Statement/v1") || len(statement.Predicate) == 0 {
				return invalid()
			}
			statementSubject := len(statement.Subject) == 1 && statement.Subject[0].Digest["sha256"] == strings.TrimPrefix(evidence.ImageManifestDigest, "sha256:")
			// BuildKit's legacy OCI attestation layout leaves the in-toto subject
			// empty and cryptographically binds the attestation manifest through
			// the content index's vnd.docker.reference.digest annotation instead.
			legacyIndexSubject := len(statement.Subject) == 0 && attestation.manifest.Subject == nil && attestation.descriptor.Annotations["vnd.docker.reference.type"] == "attestation-manifest" && reference == evidence.ImageManifestDigest
			if !statementSubject && !legacyIndexSubject {
				return invalid()
			}
			if declared := layer.Annotations["in-toto.io/predicate-type"]; declared != "" && declared != statement.PredicateType {
				return invalid()
			}
			switch statement.PredicateType {
			case "https://slsa.dev/provenance/v0.2", "https://slsa.dev/provenance/v1":
				var buildType string
				expectedBuildType := "https://mobyproject.org/buildkit@v1"
				if statement.PredicateType == "https://slsa.dev/provenance/v0.2" {
					_ = json.Unmarshal(statement.Predicate["buildType"], &buildType)
				} else {
					expectedBuildType = "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md"
					var definition struct{ BuildType string }
					_ = json.Unmarshal(statement.Predicate["buildDefinition"], &definition)
					buildType = definition.BuildType
				}
				if buildType != expectedBuildType {
					return invalid()
				}
				if evidence.ProvenanceDigest != "" {
					return invalid()
				}
				evidence.ProvenanceDigest = layer.Digest
			case "https://spdx.dev/Document":
				var version, id string
				if json.Unmarshal(statement.Predicate["spdxVersion"], &version) != nil || json.Unmarshal(statement.Predicate["SPDXID"], &id) != nil || (version != "SPDX-2.2" && version != "SPDX-2.3") || id != "SPDXRef-DOCUMENT" || evidence.SBOMDigest != "" {
					return invalid()
				}
				evidence.SBOMDigest = layer.Digest
			}
		}
	}
	if evidence.ProvenanceDigest == "" || evidence.SBOMDigest == "" {
		return invalid()
	}
	return evidence, nil
}
