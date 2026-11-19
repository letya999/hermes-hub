//go:build integration

package main

import (
	"context"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *m32Lifecycle) memoryRightFences(t *testing.T, ctx context.Context, runs [2]api.RunContext) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var sessions [2]*mcp.ClientSession
	for i, user := range h.f.Users {
		sessions[i] = h.session(t, ctx, user, runs[i])
		defer sessions[i].Close()
	}
	docs, err := catalog.Generate()
	m32Require(t, err)
	gov := toolhub.NewGovernanceStore(filepath.Join(filepath.Dir(h.f.StorePath), "governance.json"))
	for i, user := range h.f.Users {
		for _, right := range []struct{ capability, operation string }{{"tools", "use"}, {"memory", "search"}, {"memory", "remember"}} {
			id := api.DeterministicID("grant", user, right.capability, right.operation)
			m32Require(t, gov.RevokeRight(id, "operator", "M32 open-session right fence", time.Now()))
			tool, args := "memory_search", map[string]any{"query": "M32-private-marker-" + user}
			if right.operation == "remember" {
				tool, args = "memory_card", map[string]any{"op": "remember", "name": "M32 acceptance note", "text": "M32-remembered-" + user}
			}
			m32SessionCall(t, ctx, sessions[i], tool, args, false)
			other := h.f.Users[1-i]
			body := m32SessionCall(t, ctx, sessions[1-i], "memory_search", map[string]any{"query": "M32-private-marker-" + other}, true)
			if !strings.Contains(body, "M32-private-marker-"+other) || strings.Contains(body, "M32-private-marker-"+user) {
				t.Fatal("revoking one owner disrupted or exposed the other owner's memory")
			}
			grant := api.NewRightGrant(api.DeterministicID("grant-recovery", user, right.capability, right.operation), user, right.capability, right.operation, "", "M32 open-session recovery", "operator", time.Now().Add(time.Hour))
			m32Require(t, api.Confirm(&grant, "operator", time.Now()))
			m32Require(t, gov.GrantRight(grant, docs.Rights, time.Now()))
			m32SessionCall(t, ctx, sessions[i], tool, args, true)
		}
	}
	t.Log("open sessions: tools.use/memory.search/memory.remember revoke, sibling isolation and confirmed regrant exercised")
}
