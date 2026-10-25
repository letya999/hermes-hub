package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/netguard"
)

func ExactCommitMetadata(value any) string {
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for _, key := range []string{"commit", "revision", "commitSHA", "sourceCommit"} {
				if candidate, ok := typed[key].(string); ok && gitSHAPattern.MatchString(strings.ToLower(candidate)) {
					return strings.ToLower(candidate)
				}
			}
			for _, nested := range typed {
				if candidate := visit(nested); candidate != "" {
					return candidate
				}
			}
		case []any:
			for _, nested := range typed {
				if candidate := visit(nested); candidate != "" {
					return candidate
				}
			}
		}
		return ""
	}
	return visit(value)
}

type ociProof struct {
	Image            string
	ManifestDigest   string
	Source           string
	Revision         string
	Version          string
	Entrypoint       []string
	Health           HealthProbe
	ProvenanceDigest string
	SBOMDigest       string
	Evidence         []RecipeEvidence
}

func CompleteRecipeCandidate(ctx context.Context, client *http.Client, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool) {
	return completeRecipeCandidateAt(ctx, client, "", candidate, lookup)
}

func completeRecipeCandidateAt(ctx context.Context, client *http.Client, registryBase string, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool) {
	completed, accepted, _ := VerifyRecipeCandidateAt(ctx, client, registryBase, candidate, lookup)
	return completed, accepted
}

func VerifyRecipeCandidateAt(ctx context.Context, client *http.Client, registryBase string, candidate RecipeCandidate, lookup RecipeLookup) (RecipeCandidate, bool, error) {
	if candidate.Launch.Transport != ContainerMCP {
		return candidate, candidate.CommitSHA != "" && strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA), nil
	}
	proof, err := InspectPublishedOCIAt(ctx, client, registryBase, candidate.Launch.Artifact)
	if err != nil {
		return RecipeCandidate{}, false, err
	}
	if candidate.Launch.Digest != "" && candidate.Launch.Digest != proof.ManifestDigest {
		return RecipeCandidate{}, false, fmt.Errorf("%w: selected OCI digest changed", ErrStale)
	}
	if !sameRepository(proof.Source, lookup.Repository) || !strings.EqualFold(proof.Revision, lookup.CommitSHA) {
		return RecipeCandidate{}, false, nil
	}
	if candidate.CommitSHA != "" && !strings.EqualFold(candidate.CommitSHA, lookup.CommitSHA) {
		return RecipeCandidate{}, false, nil
	}
	if len(lookup.VersionHints) > 0 && proof.Version != "" && !ContainsFold(lookup.VersionHints, proof.Version) {
		return RecipeCandidate{}, false, nil
	}
	candidate.Repository = lookup.Repository
	candidate.Subfolder = lookup.Subfolder
	candidate.CommitSHA = lookup.CommitSHA
	candidate.Launch.Artifact = proof.Image
	candidate.Launch.Digest = proof.ManifestDigest
	if candidate.Launch.Version == "" {
		candidate.Launch.Version = proof.Version
	}
	if len(candidate.Launch.Entrypoint) == 0 {
		candidate.Launch.Entrypoint = proof.Entrypoint
	}
	if candidate.Launch.Health.Value == "" {
		candidate.Launch.Health = proof.Health
	}
	candidate.Launch.Evidence = append(candidate.Launch.Evidence, proof.Evidence...)
	candidate.Evidence = append(candidate.Evidence, proof.Evidence...)
	return candidate, LaunchReady(candidate.Launch), nil
}

func ContainsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimPrefix(value, "v"), strings.TrimPrefix(wanted, "v")) {
			return true
		}
	}
	return false
}

type OciRegistryClient struct {
	HTTP         *http.Client
	RegistryBase string
}

func InspectPublishedOCI(ctx context.Context, client *http.Client, raw string) (ociProof, error) {
	return InspectPublishedOCIAt(ctx, client, "", raw)
}

func InspectPublishedOCIAt(ctx context.Context, client *http.Client, registryBase, raw string) (ociProof, error) {
	ref, err := ParseOCIReference(raw)
	if err != nil {
		return ociProof{}, err
	}
	registry := OciRegistryClient{HTTP: client, RegistryBase: registryBase}
	manifestBody, headers, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(ref.Reference), "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
	if err != nil {
		return ociProof{}, err
	}
	manifestDigest := headers.Get("Docker-Content-Digest")
	if manifestDigest == "" {
		hash := sha256.Sum256(manifestBody)
		manifestDigest = "sha256:" + hex.EncodeToString(hash[:])
	}
	if !DigestPattern.MatchString(manifestDigest) {
		return ociProof{}, fmt.Errorf("%w: OCI manifest digest", ErrInvalid)
	}
	if ref.Digest != "" && ref.Digest != manifestDigest {
		return ociProof{}, fmt.Errorf("%w: OCI digest drift", ErrStale)
	}
	manifest := map[string]any{}
	if json.Unmarshal(manifestBody, &manifest) != nil {
		return ociProof{}, fmt.Errorf("%w: OCI manifest", ErrInvalid)
	}
	configDigest := ""
	var indexManifests []any
	attestationSubject := manifestDigest
	if config, ok := manifest["config"].(map[string]any); ok {
		configDigest = firstString(config, "digest")
	}
	if manifests, ok := manifest["manifests"].([]any); ok && len(manifests) > 0 {
		indexManifests = manifests
		child := ChooseOCIManifest(manifests)
		if child == "" {
			return ociProof{}, fmt.Errorf("%w: OCI platform manifest", ErrInvalid)
		}
		attestationSubject = child
		childBody, _, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/manifests/"+url.PathEscape(child), "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		if err != nil {
			return ociProof{}, err
		}
		var selected map[string]any
		if json.Unmarshal(childBody, &selected) != nil {
			return ociProof{}, fmt.Errorf("%w: OCI platform manifest", ErrInvalid)
		}
		if config, ok := selected["config"].(map[string]any); ok {
			configDigest = firstString(config, "digest")
		}
	}
	if !DigestPattern.MatchString(configDigest) {
		return ociProof{}, fmt.Errorf("%w: OCI config digest", ErrInvalid)
	}
	configBody, _, err := registry.get(ctx, ref.Registry, "/v2/"+ref.Repository+"/blobs/"+url.PathEscape(configDigest), "application/json")
	if err != nil {
		return ociProof{}, err
	}
	var config struct {
		Config struct {
			Labels      map[string]string `json:"Labels"`
			Entrypoint  []string          `json:"Entrypoint"`
			Cmd         []string          `json:"Cmd"`
			Healthcheck struct {
				Test []string `json:"Test"`
			} `json:"Healthcheck"`
		} `json:"config"`
	}
	if json.Unmarshal(configBody, &config) != nil {
		return ociProof{}, fmt.Errorf("%w: OCI config", ErrInvalid)
	}
	labels := config.Config.Labels
	if labels == nil {
		return ociProof{}, fmt.Errorf("%w: OCI provenance labels", ErrUnauthorized)
	}
	provenanceDigest, sbomDigest, evidence, err := registry.attestations(ctx, ref.Registry, ref.Repository, attestationSubject)
	if err != nil && len(indexManifests) > 0 {
		provenanceDigest, sbomDigest, evidence, err = registry.embeddedAttestations(ctx, ref.Registry, ref.Repository, indexManifests, attestationSubject)
	}
	if err != nil {
		return ociProof{}, err
	}
	entrypoint := append(append([]string(nil), config.Config.Entrypoint...), config.Config.Cmd...)
	for _, arg := range entrypoint {
		if len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") || strings.Contains(arg, "${") {
			return ociProof{}, fmt.Errorf("%w: OCI entrypoint", ErrInvalid)
		}
	}
	proof := ociProof{Image: ref.Image, ManifestDigest: manifestDigest, Source: firstString(labelsAny(labels), "org.opencontainers.image.source", "org.opencontainers.image.url", "org.opencontainers.image.repository"), Revision: strings.ToLower(labels["org.opencontainers.image.revision"]), Version: labels["org.opencontainers.image.version"], Entrypoint: entrypoint, ProvenanceDigest: provenanceDigest, SBOMDigest: sbomDigest, Evidence: evidence}
	if len(config.Config.Healthcheck.Test) > 1 && strings.EqualFold(config.Config.Healthcheck.Test[0], "CMD") {
		check := config.Config.Healthcheck.Test[1:]
		if len(check) > 0 && ValidCommand(check[0]) {
			proof.Health = HealthProbe{Kind: "exec", Value: strings.Join(check, " "), TimeoutSeconds: 5}
		}
	}
	if !strings.HasPrefix(strings.ToLower(proof.Source), "https://github.com/") || !gitSHAPattern.MatchString(proof.Revision) {
		return ociProof{}, fmt.Errorf("%w: OCI source provenance", ErrUnauthorized)
	}
	return proof, nil
}

func ChooseOCIManifest(values []any) string {
	for _, raw := range values {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		platform, _ := value["platform"].(map[string]any)
		if strings.EqualFold(firstString(platform, "os"), "linux") && (strings.EqualFold(firstString(platform, "architecture"), "amd64") || strings.EqualFold(firstString(platform, "architecture"), "arm64")) {
			return firstString(value, "digest")
		}
	}
	return ""
}

type ociReference struct {
	Registry, Repository, Reference, Digest, Image string
}

func ParseOCIReference(raw string) (ociReference, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n \t") || strings.Contains(raw, "..") {
		return ociReference{}, fmt.Errorf("%w: OCI reference", ErrInvalid)
	}
	name := raw
	ref := ""
	if at := strings.LastIndex(raw, "@"); at > 0 {
		name, ref = raw[:at], raw[at+1:]
	} else if colon := strings.LastIndex(raw, ":"); colon > strings.LastIndex(raw, "/") {
		name, ref = raw[:colon], raw[colon+1:]
	}
	if name == "" || ref == "" {
		return ociReference{}, fmt.Errorf("%w: OCI tag or digest required", ErrInvalid)
	}
	parts := strings.Split(name, "/")
	registry := "registry-1.docker.io"
	repository := name
	if len(parts) > 1 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":") || parts[0] == "localhost") {
		registry, repository = strings.ToLower(parts[0]), strings.Join(parts[1:], "/")
	}
	if repository == "" || strings.HasPrefix(repository, "/") || strings.HasSuffix(repository, "/") {
		return ociReference{}, fmt.Errorf("%w: OCI repository", ErrInvalid)
	}
	digest := ""
	if strings.HasPrefix(ref, "sha256:") {
		if !DigestPattern.MatchString(ref) {
			return ociReference{}, fmt.Errorf("%w: OCI digest", ErrInvalid)
		}
		digest = ref
	}
	image := name
	return ociReference{Registry: registry, Repository: repository, Reference: ref, Digest: digest, Image: image}, nil
}

func (c OciRegistryClient) get(ctx context.Context, registry, requestPath, accept string) ([]byte, http.Header, error) {
	if registry == "docker.io" {
		registry = "registry-1.docker.io"
	}
	client := c.HTTP
	if client == nil {
		endpointHost := registry
		if c.RegistryBase != "" {
			if parsed, err := url.Parse(c.RegistryBase); err == nil && parsed.Hostname() != "" {
				endpointHost = parsed.Hostname()
			}
		}
		client = OciHTTPClient(registry, endpointHost)
	}
	endpoint := "https://" + registry + requestPath
	if c.RegistryBase != "" {
		endpoint = strings.TrimRight(c.RegistryBase, "/") + requestPath
	}
	request := func(token string) ([]byte, http.Header, int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		req.Header.Set("Accept", accept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, nil, 0, err
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
		if readErr != nil || len(body) > 8<<20 {
			return nil, response.Header, response.StatusCode, fmt.Errorf("%w: oversized OCI response", ErrInvalid)
		}
		return body, response.Header, response.StatusCode, nil
	}
	body, headers, status, err := request("")
	if err != nil {
		return nil, nil, err
	}
	if status == http.StatusUnauthorized {
		token, tokenErr := c.token(ctx, client, headers.Get("WWW-Authenticate"))
		if tokenErr != nil {
			return nil, nil, tokenErr
		}
		body, headers, status, err = request(token)
		if err != nil {
			return nil, nil, err
		}
	}
	if status != http.StatusOK {
		return nil, headers, fmt.Errorf("%w: OCI registry status %d", ErrInvalid, status)
	}
	return body, headers, nil
}

func OciHTTPClient(registry, endpointHost string) *http.Client {
	allowed := map[string]bool{strings.ToLower(endpointHost): true, strings.ToLower(registry): true}
	if registry == "registry-1.docker.io" {
		allowed["auth.docker.io"] = true
		allowed["auth.docker.com"] = true
	}
	return &http.Client{Transport: netguard.Transport(false), Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		host := strings.ToLower(request.URL.Hostname())
		redirectAllowed := allowed[host]
		if registry == "registry-1.docker.io" && strings.HasSuffix(host, ".cloudfront.docker.com") {
			redirectAllowed = true
		}
		if registry == "ghcr.io" && strings.HasSuffix(host, ".githubusercontent.com") {
			redirectAllowed = true
		}
		if request.URL.Scheme != "https" || !redirectAllowed {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

func (c OciRegistryClient) token(ctx context.Context, client *http.Client, challenge string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", fmt.Errorf("%w: OCI registry authentication challenge", ErrUnauthorized)
	}
	values := map[string]string{}
	for _, item := range strings.Split(challenge[len("Bearer "):], ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(item), "=")
		if ok {
			values[key] = strings.Trim(value, "\"")
		}
	}
	realm := values["realm"]
	if realm == "" {
		return "", fmt.Errorf("%w: OCI token realm", ErrUnauthorized)
	}
	query := url.Values{}
	if values["service"] != "" {
		query.Set("service", values["service"])
	}
	if values["scope"] != "" {
		query.Set("scope", values["scope"])
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: OCI token status", ErrUnauthorized)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload) != nil {
		return "", fmt.Errorf("%w: OCI token response", ErrUnauthorized)
	}
	if payload.Token == "" {
		payload.Token = payload.AccessToken
	}
	if payload.Token == "" {
		return "", fmt.Errorf("%w: OCI token missing", ErrUnauthorized)
	}
	return payload.Token, nil
}

func (c OciRegistryClient) attestations(ctx context.Context, registry, repository, subject string) (string, string, []RecipeEvidence, error) {
	body, _, err := c.get(ctx, registry, "/v2/"+repository+"/referrers/"+url.PathEscape(subject), "application/vnd.oci.image.index.v1+json")
	if err != nil {
		return "", "", nil, err
	}
	var index struct {
		Manifests []struct {
			Digest       string            `json:"digest"`
			ArtifactType string            `json:"artifactType"`
			Annotations  map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.Unmarshal(body, &index) != nil {
		return "", "", nil, fmt.Errorf("%w: OCI referrers", ErrInvalid)
	}
	var provenance, sbom string
	var evidence []RecipeEvidence
	for _, descriptor := range index.Manifests {
		if !DigestPattern.MatchString(descriptor.Digest) {
			continue
		}
		kind := strings.ToLower(descriptor.ArtifactType)
		for _, value := range descriptor.Annotations {
			kind += " " + strings.ToLower(value)
		}
		switch {
		case strings.Contains(kind, "sbom") || strings.Contains(kind, "spdx") || strings.Contains(kind, "cyclonedx"):
			if sbom == "" {
				sbom = descriptor.Digest
				evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: sbom, Detail: "immutable SBOM referrer"})
			}
		case strings.Contains(kind, "provenance") || strings.Contains(kind, "slsa") || strings.Contains(kind, "in-toto"):
			if provenance == "" {
				provenance = descriptor.Digest
				evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: provenance, Detail: "immutable provenance referrer"})
			}
		}
	}
	if provenance == "" || sbom == "" {
		return "", "", nil, fmt.Errorf("%w: OCI provenance and SBOM are required", ErrUnauthorized)
	}
	return provenance, sbom, evidence, nil
}

func (c OciRegistryClient) embeddedAttestations(ctx context.Context, registry, repository string, manifests []any, subject string) (string, string, []RecipeEvidence, error) {
	var provenance, sbom string
	var evidence []RecipeEvidence
	for _, raw := range manifests {
		descriptor, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		annotations := AnyStringMap(descriptor["annotations"])
		if firstString(annotations, "vnd.docker.reference.digest") != subject || firstString(annotations, "vnd.docker.reference.type") != "attestation-manifest" {
			continue
		}
		digest := firstString(descriptor, "digest")
		if !DigestPattern.MatchString(digest) {
			continue
		}
		body, _, err := c.get(ctx, registry, "/v2/"+repository+"/manifests/"+url.PathEscape(digest), "application/vnd.oci.image.manifest.v1+json")
		if err != nil {
			continue
		}
		var document struct {
			Layers []struct {
				MediaType   string            `json:"mediaType"`
				Annotations map[string]string `json:"annotations"`
			} `json:"layers"`
		}
		if json.Unmarshal(body, &document) != nil {
			continue
		}
		kind := ""
		for _, layer := range document.Layers {
			kind += " " + strings.ToLower(layer.MediaType)
			for key, value := range layer.Annotations {
				kind += " " + strings.ToLower(key) + " " + strings.ToLower(value)
			}
		}
		if sbom == "" && (strings.Contains(kind, "sbom") || strings.Contains(kind, "spdx") || strings.Contains(kind, "cyclonedx")) {
			sbom = digest
			evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: sbom, Detail: "immutable embedded SBOM attestation"})
		}
		if provenance == "" && (strings.Contains(kind, "provenance") || strings.Contains(kind, "slsa") || strings.Contains(kind, "in-toto")) {
			provenance = digest
			evidence = append(evidence, RecipeEvidence{Source: "oci", Digest: provenance, Detail: "immutable embedded provenance attestation"})
		}
	}
	if provenance == "" || sbom == "" {
		return "", "", nil, fmt.Errorf("%w: OCI provenance and SBOM are required", ErrUnauthorized)
	}
	return provenance, sbom, evidence, nil
}

func labelsAny(labels map[string]string) map[string]any {
	result := make(map[string]any, len(labels))
	for key, value := range labels {
		result[key] = value
	}
	return result
}
