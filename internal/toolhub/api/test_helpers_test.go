package api

import "net/http"

func remoteDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: RemoteMCP,
		Source:      DefinitionSource{URL: "https://example.com/mcp", TLSMode: "required"},
		Tools:       []ToolSpec{{Name: "search", Effect: ReadEffect}, {Name: "send", Effect: WriteEffect}},
		Credentials: []CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    WorkloadPolicy{Class: PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.com"}},
		Health:      HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func repeatHex(value byte) string {
	result := make([]byte, 64)
	for i := range result {
		result[i] = value
	}
	return string(result)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func githubCommitURL() string {
	return "https://github.com/example/mcp/commit/0123456789abcdef0123456789abcdef01234567"
}

type calendarFixtureTransport struct {
	endpoint string
	base     http.RoundTripper
}

func (f calendarFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copyRequest := r.Clone(r.Context())
	copyURL := *r.URL
	copyRequest.URL = &copyURL
	fixtureReq, _ := http.NewRequest(http.MethodGet, f.endpoint, nil)
	copyRequest.URL.Scheme, copyRequest.URL.Host = fixtureReq.URL.Scheme, fixtureReq.URL.Host
	return f.base.RoundTrip(copyRequest)
}

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
