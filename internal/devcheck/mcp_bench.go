package devcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpBenchResult struct {
	Calls  int     `json:"calls"`
	MeanMS float64 `json:"mean_ms"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	MinMS  float64 `json:"min_ms"`
	MaxMS  float64 `json:"max_ms"`
}

// MCPBench measures steady-state calls over one MCP session, excluding process startup.
func MCPBench(ctx context.Context, endpoint, tool string, calls int, out io.Writer) error {
	if calls < 2 || calls > 10000 {
		return fmt.Errorf("calls must be between 2 and 10000")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if token := os.Getenv("HUB_TOOLHUB_TOKEN"); token != "" {
		httpClient.Transport = bearerTransport{token: token}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hermes-hub-bench", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
	if err != nil {
		return err
	}
	defer session.Close()

	samples := make([]float64, 0, calls)
	arguments := map[string]any{"message": "benchmark"}
	if raw := os.Getenv("HUB_MCP_BENCH_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
			return fmt.Errorf("HUB_MCP_BENCH_ARGS: %w", err)
		}
	}
	for range calls {
		start := time.Now()
		result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: arguments})
		if callErr != nil {
			return callErr
		}
		if result.IsError {
			return fmt.Errorf("tool %q returned an error", tool)
		}
		samples = append(samples, float64(time.Since(start).Microseconds())/1000)
	}
	sort.Float64s(samples)
	var sum float64
	for _, sample := range samples {
		sum += sample
	}
	result := mcpBenchResult{Calls: calls, MeanMS: sum / float64(calls), P50MS: samples[calls/2], P95MS: samples[(calls*95-1)/100], MinMS: samples[0], MaxMS: samples[calls-1]}
	return json.NewEncoder(out).Encode(result)
}
