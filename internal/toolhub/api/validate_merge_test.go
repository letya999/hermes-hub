package api

import (
	"strings"
	"testing"
	"time"
)

func validCredentialRef() CredentialReference {
	return CredentialReference{
		Schema: SchemaVersion, CredentialRefID: "cred-1", ConnectionID: "conn-1",
		Revision: 1, Backend: "vault", Locator: "vault://x", Keys: []string{"API_TOKEN"},
		Status: ActiveStatus,
	}
}

func TestCredentialReferenceValidate(t *testing.T) {
	if err := validCredentialRef().Validate(); err != nil {
		t.Fatal(err)
	}
	bad := validCredentialRef()
	bad.Locator = "line\nbreak"
	if err := bad.Validate(); err == nil {
		t.Fatal("locator with newline accepted")
	}
	bad = validCredentialRef()
	bad.Keys = []string{"API_TOKEN", "API_TOKEN"}
	if err := bad.Validate(); err == nil {
		t.Fatal("duplicate key accepted")
	}
	bad = validCredentialRef()
	bad.Keys = []string{"lower"}
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid key accepted")
	}
	bad = validCredentialRef()
	bad.Status = "weird"
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid status accepted")
	}
	// broker backend rules
	broker := validCredentialRef()
	broker.Backend = "credential-broker"
	if err := broker.Validate(); err == nil {
		t.Fatal("broker ref without contract accepted")
	}
	broker.BrokerContractID = "contract-1"
	broker.BrokerContractRevision = 1
	broker.BrokerGrantID = "grant-1"
	broker.BrokerEnv = map[string]string{"API_TOKEN": "BROKER_API_TOKEN"}
	if err := broker.Validate(); err != nil {
		t.Fatalf("valid broker ref: %v", err)
	}
	broker.BrokerEnv = map[string]string{"bad name": "X"}
	if err := broker.Validate(); err == nil {
		t.Fatal("invalid broker env accepted")
	}
	broker.BrokerEnv = map[string]string{"API_TOKEN": "BROKER_API_TOKEN"}
	broker.BrokerContextID = "ctx-team-1"
	if err := broker.Validate(); err != nil {
		t.Fatalf("broker context ref: %v", err)
	}
	broker.BrokerContextID = "not valid!!"
	if err := broker.Validate(); err == nil {
		t.Fatal("invalid broker context accepted")
	}
	if CredentialReferenceID("conn-1", 3) == "" {
		t.Fatal("deterministic ref id empty")
	}
}

func TestConnectionValidate(t *testing.T) {
	valid := Connection{
		Schema: SchemaVersion, ConnectionID: "conn-1", DefinitionID: "def-1",
		Owner: OwnerRef{Type: ContextOwner, ID: "ctx-1"}, Revision: 1, Status: ActiveStatus,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, status := range []Status{DisabledStatus, RevokedStatus, DegradedStatus} {
		c := valid
		c.Status = status
		if err := c.Validate(); err != nil {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	bad := valid
	bad.Owner = OwnerRef{}
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid owner accepted")
	}
	bad = valid
	bad.CredentialRefID = "not valid!!"
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid credential ref accepted")
	}
	bad = valid
	bad.Metadata = map[string]string{"api_token_hint": "x"}
	if err := bad.Validate(); err == nil {
		t.Fatal("secret-like metadata key accepted")
	}
	bad = valid
	bad.Metadata = map[string]string{"mcp_endpoint": "http://insecure"}
	if err := bad.Validate(); err == nil {
		t.Fatal("insecure mcp_endpoint accepted")
	}
}

func validBinding() ToolBinding {
	return ToolBinding{
		Schema: SchemaVersion, ToolBindingID: "bind-1", PrincipalID: "user-1",
		ContextID: "ctx-1", RuntimeID: "rt-1", DefinitionID: "def-1",
		DefinitionVersion: "1.0.0", PolicyVersion: "pol-1", Revision: 1,
		ProjectionRevision: 1, WorkloadClass: PerUser, Status: ActiveStatus,
	}
}

func TestToolBindingValidate(t *testing.T) {
	if err := validBinding().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, class := range []WorkloadClass{Shared, PerJob} {
		b := validBinding()
		b.WorkloadClass = class
		if err := b.Validate(); err != nil {
			t.Fatalf("class %s: %v", class, err)
		}
	}
	bad := validBinding()
	bad.WorkloadClass = "bogus"
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid class accepted")
	}
	bad = validBinding()
	bad.CredentialRefID = "cred-1"
	if err := bad.Validate(); err == nil {
		t.Fatal("credential ref without connection accepted")
	}
	bad = validBinding()
	bad.ConnectionID = "conn-1"
	bad.ConnectionRevision = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("connection without revision accepted")
	}
	bad = validBinding()
	bad.ConnectionID = "conn-1"
	bad.ConnectionRevision = 2
	bad.CredentialRefID = "cred-1"
	bad.CredentialRevision = 3
	if err := bad.Validate(); err != nil {
		t.Fatalf("bound credential: %v", err)
	}
	first, second := DeterministicBindingID("p", "c", "r", "d", "1.0.0", "conn", "cred"), DeterministicBindingID("p", "c", "r", "d", "1.0.0", "conn", "cred")
	if first != second {
		t.Fatal("binding id not deterministic")
	}
	if first == DeterministicBindingID("p", "c", "r", "d", "1.0.0", "conn", "other") {
		t.Fatal("binding id ignores credential")
	}
	if !strings.HasPrefix(ProjectedToolName("def-1", "1.0.0", "list items"), "hub-def-1-list-items-") {
		t.Fatalf("projected name: %s", ProjectedToolName("def-1", "1.0.0", "list items"))
	}
	if !DefinitionsEqual(ToolDefinition{DefinitionID: "a"}, ToolDefinition{DefinitionID: "a"}) ||
		DefinitionsEqual(ToolDefinition{DefinitionID: "a"}, ToolDefinition{DefinitionID: "b"}) {
		t.Fatal("DefinitionsEqual wrong")
	}
	if !RecordsEqual(map[string]int{"x": 1}, map[string]int{"x": 1}) {
		t.Fatal("RecordsEqual wrong")
	}
	if slug("UPPER_Spaced!!Name") != "upper-spaced--name" || slug("!!!") != "tool" {
		t.Fatalf("slug: %q %q", slug("UPPER_Spaced!!Name"), slug("!!!"))
	}
}

func TestWorkloadInstanceLifecycle(t *testing.T) {
	owner := &OwnerRef{Type: ContextOwner, ID: "ctx-1"}
	binding := validBinding()
	binding.ConnectionID = "conn-1"
	binding.ConnectionRevision = 1
	now := time.Now()
	w, err := NewWorkloadInstance(binding, owner, "", 2, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != StartingStatus || w.WorkloadID == "" || w.RuntimeID != "rt-1" {
		t.Fatalf("workload: %+v", w)
	}
	// per-user without connection keys the workload by binding
	noConn := validBinding()
	w2, err := NewWorkloadInstance(noConn, owner, "", 1, now, time.Time{})
	if err != nil || w2.WorkloadID == w.WorkloadID {
		t.Fatalf("unconnected workload: %v %+v", err, w2)
	}
	// per-job needs owner + expiry
	job := validBinding()
	job.WorkloadClass = PerJob
	if _, err := NewWorkloadInstance(job, owner, "job-1", 1, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("per-job: %v", err)
	}
	if _, err := NewWorkloadInstance(job, owner, "job-1", 1, now, time.Time{}); err == nil {
		t.Fatal("per-job without expiry accepted")
	}
	// shared cannot carry owner/runtime
	shared := validBinding()
	shared.WorkloadClass = Shared
	w3, err := NewWorkloadInstance(shared, nil, "", 1, now, time.Time{})
	if err != nil || w3.RuntimeID != "" {
		t.Fatalf("shared workload: %v %+v", err, w3)
	}
	w3.RuntimeID = "rt-1"
	if err := w3.Validate(); err == nil {
		t.Fatal("shared workload with runtime accepted")
	}
	w3 = WorkloadInstance{Schema: SchemaVersion, WorkloadID: "w-1", BindingID: "b-1",
		DefinitionID: "d-1", DefinitionVersion: "1.0.0", Generation: 1,
		Class: PerUser, Status: "bogus"}
	if err := w3.Validate(); err == nil {
		t.Fatal("invalid status accepted")
	}
}
