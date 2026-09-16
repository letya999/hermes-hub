package toolhub

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func ociFixture(t *testing.T, scenario string) ([]byte, string) {
	t.Helper()
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)}
	addBlob := func(media string, data []byte) map[string]any {
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		files["blobs/sha256/"+strings.TrimPrefix(digest, "sha256:")] = data
		return map[string]any{"mediaType": media, "digest": digest, "size": len(data)}
	}
	add := func(media string, value any) map[string]any {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return addBlob(media, data)
	}
	layer := addBlob("application/vnd.oci.image.layer.v1.tar", languageContext(t, map[string]string{"mcp-server": "synthetic layer fixture"}))
	config := add("application/vnd.oci.image.config.v1+json", map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{layer["digest"].(string)}}})
	if scenario == "config-size" {
		config["size"] = 123
	}
	image := add(ociManifestType, map[string]any{"schemaVersion": 2, "mediaType": ociManifestType, "config": config, "layers": []any{layer}})
	image["platform"] = map[string]string{"os": "linux", "architecture": "amd64"}
	imageDigest := image["digest"].(string)
	subjectDigest := strings.TrimPrefix(imageDigest, "sha256:")
	if scenario == "subject-drift" {
		subjectDigest = strings.Repeat("a", 64)
	}
	statement := func(predicateType string, predicate any) map[string]any {
		if scenario == "legacy-empty-subject" || scenario == "legacy-empty-subject-reference-drift" {
			return map[string]any{"_type": "https://in-toto.io/Statement/v0.1", "subject": []any{}, "predicateType": predicateType, "predicate": predicate}
		}
		return map[string]any{"_type": "https://in-toto.io/Statement/v1", "subject": []any{map[string]any{"name": "_", "digest": map[string]string{"sha256": subjectDigest}}}, "predicateType": predicateType, "predicate": predicate}
	}
	provenanceType := "https://slsa.dev/provenance/v0.2"
	provenancePredicate := map[string]any{"buildType": "https://mobyproject.org/buildkit@v1"}
	if scenario == "provenance-v1" {
		provenanceType = "https://slsa.dev/provenance/v1"
		provenancePredicate = map[string]any{"buildDefinition": map[string]any{"buildType": "https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md"}}
	}
	if scenario == "invalid-provenance" {
		provenancePredicate = map[string]any{"claim": "not provenance"}
	}
	provenance := add("application/vnd.in-toto+json", statement(provenanceType, provenancePredicate))
	sbom := add("application/vnd.in-toto+json", statement("https://spdx.dev/Document", map[string]any{"SPDXID": "SPDXRef-DOCUMENT", "spdxVersion": "SPDX-2.3", "packages": []any{}}))
	layers := []any{provenance, sbom}
	if scenario == "missing-provenance" {
		layers = layers[1:]
	}
	if scenario == "missing-sbom" {
		layers = layers[:1]
	}
	if scenario == "duplicate-provenance" {
		layers = append(layers, provenance)
	}
	attestationBody := map[string]any{"schemaVersion": 2, "mediaType": ociManifestType, "config": add("application/vnd.oci.image.config.v1+json", map[string]any{"os": "unknown", "architecture": "unknown", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{}}}), "layers": layers}
	if scenario == "artifact" || scenario == "wrapped" || scenario == "embedded" {
		attestationBody["artifactType"] = "application/vnd.docker.attestation.manifest.v1+json"
		attestationBody["subject"] = image
		emptyConfig := add("application/vnd.oci.empty.v1+json", map[string]any{})
		if scenario == "embedded" {
			emptyConfig["data"] = []byte("{}")
			delete(files, "blobs/sha256/"+strings.TrimPrefix(emptyConfig["digest"].(string), "sha256:"))
		}
		attestationBody["config"] = emptyConfig
	}
	attestation := add(ociManifestType, attestationBody)
	reference := imageDigest
	if scenario == "reference-drift" || scenario == "legacy-empty-subject-reference-drift" {
		reference = "sha256:" + strings.Repeat("a", 64)
	}
	attestation["annotations"] = map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": reference}
	attestation["platform"] = map[string]string{"os": "unknown", "architecture": "unknown"}
	if scenario == "runnable-attestation" {
		attestation["platform"] = image["platform"]
	}
	index := map[string]any{"schemaVersion": 2, "mediaType": ociIndexType, "manifests": []any{image, attestation}}
	if scenario == "wrapped" {
		index = map[string]any{"schemaVersion": 2, "manifests": []any{add(ociIndexType, index)}}
	}
	files["index.json"], _ = json.Marshal(index)
	if scenario == "blob-drift" {
		files["blobs/sha256/"+strings.TrimPrefix(imageDigest, "sha256:")] = []byte("corrupted")
	}
	if scenario == "missing-blob" {
		delete(files, "blobs/sha256/"+strings.TrimPrefix(imageDigest, "sha256:"))
	}
	if scenario == "layout-version" {
		files["oci-layout"] = []byte(`{"imageLayoutVersion":"2.0.0"}`)
	}
	var output bytes.Buffer
	w := tar.NewWriter(&output)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "symlink" {
		_ = w.WriteHeader(&tar.Header{Name: "host", Typeflag: tar.TypeSymlink, Linkname: "/host"})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if scenario == "trailing" {
		output.WriteString("unreviewed")
	}
	return output.Bytes(), imageDigest
}

func TestOCIArtifactEvidenceRelationships(t *testing.T) {
	for _, scenario := range []string{"legacy", "legacy-empty-subject", "artifact", "wrapped", "embedded", "provenance-v1"} {
		t.Run(scenario, func(t *testing.T) {
			data, digest := ociFixture(t, scenario)
			evidence, err := VerifyOCIArtifact(bytes.NewReader(data), int64(len(data)), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if evidence.ImageManifestDigest != digest || evidence.ImageConfigDigest == digest || evidence.ProvenanceDigest == "" || evidence.SBOMDigest == "" || evidence.Architecture != "amd64" {
				t.Fatalf("wrong evidence %+v", evidence)
			}
			if (evidence.IndexDigest != evidence.LayoutDigest) != (scenario == "wrapped") {
				t.Fatal("index and layout digests conflated")
			}
		})
	}
}

func TestOCIArtifactRejectsMissingOrDriftingEvidence(t *testing.T) {
	for _, scenario := range []string{"config-size", "subject-drift", "reference-drift", "legacy-empty-subject-reference-drift", "missing-provenance", "missing-sbom", "duplicate-provenance", "invalid-provenance", "runnable-attestation", "blob-drift", "missing-blob", "layout-version", "symlink", "trailing"} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := ociFixture(t, scenario)
			if _, err := VerifyOCIArtifact(bytes.NewReader(data), int64(len(data)), 1<<20); err == nil {
				t.Fatal("invalid artifact accepted")
			}
		})
	}
	data, _ := ociFixture(t, "legacy")
	for _, limit := range []int64{0, 1, 9 << 30} {
		if _, err := VerifyOCIArtifact(bytes.NewReader(data), int64(len(data)), limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if _, err := VerifyOCIArtifact(nil, 100, 1000); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := VerifyOCIArtifact(bytes.NewReader([]byte("invalid")), 7, 1000); err == nil {
		t.Fatal("invalid tar accepted")
	}
}
