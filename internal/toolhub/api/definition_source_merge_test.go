package api

import (
	"errors"
	"strings"
	"testing"
)

func TestDefinitionSourceValidateContract(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	digest := "sha256:" + strings.Repeat("ab", 32)

	// remote/provider transports: exactly one https URL with required TLS
	remote := DefinitionSource{URL: "https://mcp.example.io/sse", TLSMode: "required"}
	if err := remote.Validate(RemoteMCP); err != nil {
		t.Fatalf("remote source: %v", err)
	}
	for name, s := range map[string]DefinitionSource{
		"http":       {URL: "http://mcp.example.io", TLSMode: "required"},
		"no tls":     {URL: "https://mcp.example.io"},
		"query":      {URL: "https://mcp.example.io/?x=1", TLSMode: "required"},
		"userinfo":   {URL: "https://u@mcp.example.io", TLSMode: "required"},
		"digest":     {URL: "https://mcp.example.io", TLSMode: "required", Digest: digest},
		"repo":       {URL: "https://mcp.example.io", TLSMode: "required", Repository: "https://github.com/a/b"},
		"two fields": {URL: "https://mcp.example.io", TLSMode: "required", Image: "img"},
	} {
		if err := s.Validate(RemoteMCP); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}

	// container transport: digest-pinned image, no mutable tag, bounded args
	container := DefinitionSource{Image: "img", Digest: digest, Repository: "https://github.com/a/b", CommitSHA: sha,
		ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest}
	if err := container.Validate(ContainerMCP); err != nil {
		t.Fatalf("container source: %v", err)
	}
	for name, s := range map[string]DefinitionSource{
		"missing digest":  {Image: "img"},
		"mutable tag":     {Image: "img:latest", Digest: digest},
		"at-image":        {Image: "img@x", Digest: digest},
		"dots":            {Image: "img..x", Digest: digest},
		"bad transport":   {Image: "img", Digest: digest, Repository: "https://github.com/a/b", CommitSHA: sha, ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest, NetworkTransport: "carrier-pigeon"},
		"network on cli":  {NetworkTransport: "sse", Command: "ls"},
		"upper sha":       {Image: "img", Digest: digest, Repository: "https://github.com/a/b", CommitSHA: strings.ToUpper(sha), ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest},
		"bad archive dig": {Image: "img", Digest: digest, ArchiveDigest: "md5:x"},
		"contract source": {Image: "img", Digest: digest, ToolContractSource: "vibes"},
	} {
		transport := ContainerMCP
		if name == "network on cli" {
			transport = BoundedCLI
		}
		if err := s.Validate(transport); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// network transport variants legal on container
	for _, nt := range []string{"sse", "http", "streamable-http"} {
		s := container
		s.NetworkTransport = nt
		if err := s.Validate(ContainerMCP); err != nil {
			t.Fatalf("network %s: %v", nt, err)
		}
	}

	// bounded CLI: bare command on tools image carries no provenance
	if err := (DefinitionSource{Command: "ripgrep"}).Validate(BoundedCLI); err != nil {
		t.Fatalf("catalog cli: %v", err)
	}
	cliArtifact := DefinitionSource{Command: "/app/tool", Image: "img", Digest: digest,
		Repository: "https://github.com/a/b", CommitSHA: sha, ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest}
	if err := cliArtifact.Validate(BoundedCLI); err != nil {
		t.Fatalf("cli artifact: %v", err)
	}
	for name, s := range map[string]DefinitionSource{
		"no command":      {},
		"command + url":   {Command: "ls", URL: "https://x.io"},
		"bad digest":      {Command: "/app/tool", Image: "img", Digest: "x", Repository: "https://github.com/a/b", CommitSHA: sha, ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest},
		"relative cmd":    {Command: "rel/tool", Image: "img", Digest: digest, Repository: "https://github.com/a/b", CommitSHA: sha, ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest},
		"no provenance":   {Command: "/app/tool", Image: "img", Digest: digest},
		"partial package": {Command: "/app/tool", Image: "img", Digest: digest, ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest, PackageRegistry: "pypi"},
		"catalog + repo":  {Command: "ls", Repository: "https://github.com/a/b"},
	} {
		if err := s.Validate(BoundedCLI); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// release-pinned CLI artifact
	release := DefinitionSource{Command: "/app/tool", Image: "img", Digest: digest,
		Repository: "https://github.com/a/b", ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest,
		ReleaseTag: "v1.2.3", ReleaseAsset: "tool-linux-amd64", AssetDigest: digest}
	if err := release.Validate(BoundedCLI); err != nil {
		t.Fatalf("release artifact: %v", err)
	}
	// package-pinned CLI artifact
	pkg := DefinitionSource{Command: "/app/tool", Image: "img", Digest: digest,
		ArchiveDigest: digest, RecipeDigest: digest, ReviewDigest: digest,
		PackageRegistry: "pypi", PackageName: "mcp-tool", PackageVersion: "1.2.3"}
	if err := pkg.Validate(BoundedCLI); err != nil {
		t.Fatalf("package artifact: %v", err)
	}
	// agent-tools carries no source at all
	if err := (DefinitionSource{}).Validate(AgentTools); err != nil {
		t.Fatalf("agent source: %v", err)
	}
	if err := (DefinitionSource{Image: "img"}).Validate(AgentTools); !errors.Is(err, ErrInvalid) {
		t.Fatal("agent source with image")
	}
	// args cannot carry secret references
	bad := remote
	bad.Args = []string{"${SECRET}"}
	if err := bad.Validate(RemoteMCP); !errors.Is(err, ErrInvalid) {
		t.Fatal("secret arg")
	}
	bad.Args = []string{strings.Repeat("x", 300)}
	if err := bad.Validate(RemoteMCP); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversized arg")
	}
	if err := (DefinitionSource{}).Validate("weird"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown transport")
	}
}
