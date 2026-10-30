package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConfirmedToolContractLifecycle(t *testing.T) {
	imported := ImportedArtifact{Definition: ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "demo-1", Version: "1.0.0", Transport: ContainerMCP,
		Source:    DefinitionSource{Image: "img", Digest: "sha256:" + strings.Repeat("a", 64)},
		Execution: ExecutionPolicy{TimeoutSeconds: 5, OutputBytes: 1024, CPUMillis: 1, MemoryMiB: 1, MaxPIDs: 1, Egress: []string{"h"}},
		Tools:     []ToolSpec{{Name: "claimed_one", Effect: ReadEffect}},
	}}
	probed := []ToolSpec{{Name: "b_tool", Effect: WriteEffect}, {Name: "a_tool", Effect: ReadEffect}}
	contract, err := ApplyPreflightToolList(&imported, probed)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Source != ToolContractPreflight || len(contract.Tools) != 2 {
		t.Fatalf("contract: %+v", contract)
	}
	if imported.Definition.Source.ToolContractDigest == "" || imported.Definition.Source.ReviewDigest == "" {
		t.Fatal("digests not stamped")
	}
	// verify detects drift
	if err := VerifyDefinitionToolContract(imported.Definition); err != nil {
		t.Fatalf("verify: %v", err)
	}
	drifted := imported.Definition
	drifted.Tools = append(drifted.Tools, ToolSpec{Name: "extra", Effect: ReadEffect})
	if err := VerifyDefinitionToolContract(drifted); !errors.Is(err, ErrStale) {
		t.Fatalf("drift accepted: %v", err)
	}
	// missing contract rejected
	if err := VerifyDefinitionToolContract(ToolDefinition{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing contract: %v", err)
	}
	// mismatched claim set rejected
	mismatch := imported
	mismatch.Definition.Tools = []ToolSpec{{Name: "other", Effect: ReadEffect}}
	if err := AttachConfirmedToolContract(&mismatch, contract); !errors.Is(err, ErrStale) {
		t.Fatalf("mismatched attach: %v", err)
	}
	// digest is canonical — tool order does not matter
	reordered := ConfirmedToolContract{Source: ToolContractPreflight, Tools: []ToolSpec{probed[1], probed[0]}}
	d1, _ := ConfirmedToolContractDigest(contract)
	d2, _ := ConfirmedToolContractDigest(reordered)
	if d1 != d2 {
		t.Fatal("digest not canonical")
	}
}

func TestPreflightToolListHonorsCuratedReadTools(t *testing.T) {
	// A prepared connector's curated read_tools declares read semantics the
	// upstream server fails to annotate (POST search/query endpoints). The
	// probe's fail-closed write verdict is upgraded for curated names only.
	imported := ImportedArtifact{Definition: ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "notion-mcp-server", Version: "1.0.0", Transport: ContainerMCP,
		Source:    DefinitionSource{Image: "img", Digest: "sha256:" + strings.Repeat("b", 64), Repository: "https://github.com/makenotion/notion-mcp-server"},
		Execution: ExecutionPolicy{TimeoutSeconds: 5, OutputBytes: 1024, CPUMillis: 1, MemoryMiB: 1, MaxPIDs: 1, Egress: []string{"api.notion.com"}},
		Tools:     []ToolSpec{{Name: "API-post-search", Effect: WriteEffect}},
	}}
	probed := []ToolSpec{
		{Name: "API-post-search", Effect: WriteEffect},
		{Name: "API-query-data-source", Effect: WriteEffect},
		{Name: "API-delete-a-block", Effect: WriteEffect},
	}
	contract, err := ApplyPreflightToolList(&imported, probed)
	if err != nil {
		t.Fatal(err)
	}
	effects := map[string]Effect{}
	for _, tool := range contract.Tools {
		effects[tool.Name] = tool.Effect
	}
	if effects["API-post-search"] != ReadEffect {
		t.Fatalf("curated read tool stayed write: %+v", contract.Tools)
	}
	if effects["API-query-data-source"] != ReadEffect {
		t.Fatalf("curated read tool stayed write: %+v", contract.Tools)
	}
	if effects["API-delete-a-block"] != WriteEffect {
		t.Fatalf("genuine write tool flipped: %+v", contract.Tools)
	}
}

func TestConfirmedToolContractValidation(t *testing.T) {
	if _, err := ConfirmedToolContractDigest(ConfirmedToolContract{Source: "guessed", Tools: []ToolSpec{{Name: "t", Effect: ReadEffect}}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("untrusted source: %v", err)
	}
	if _, err := ConfirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty contract: %v", err)
	}
	big := ConfirmedToolContract{Source: ToolContractPreflight, Tools: make([]ToolSpec, 257)}
	for i := range big.Tools {
		big.Tools[i] = ToolSpec{Name: fmt.Sprintf("tool_%d", i), Effect: ReadEffect}
	}
	if _, err := ConfirmedToolContractDigest(big); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded contract: %v", err)
	}
	if _, err := ConfirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractReviewManifest,
		Tools: []ToolSpec{{Name: "a", Effect: ReadEffect}, {Name: "a", Effect: WriteEffect}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate tool: %v", err)
	}
	if _, err := ConfirmedToolContractDigest(ConfirmedToolContract{Source: ToolContractPreflight,
		Tools: []ToolSpec{{Name: "bad name"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad name: %v", err)
	}
	if _, err := ApplyPreflightToolList(nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil artifact: %v", err)
	}
	if err := AttachConfirmedToolContract(nil, ConfirmedToolContract{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil attach: %v", err)
	}
}
