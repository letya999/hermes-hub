package agenttools

import (
	"context"
	"os"

	"github.com/letya999/hermes-hub/internal/inboundfile"
)

// inboxFetch stores a complete receipt before acknowledging the staged copy.
func (t *Tools) inboxFetch(ctx context.Context, ws *os.Root, r Input) (map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return inboundfile.Fetch(ctx, ws, "inbox/", r.ID, t.CommunicationURL, t.CommunicationAuth, "", t.HTTP, t.lock, unpackZip)
}
