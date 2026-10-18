package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/routines"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// runProbeScript runs a watcher probe script in the disposable runtime and
// requires exactly one canonical emitted value (SPEC-0059).
func (g *Gateway) runProbeScript(ctx context.Context, o RoutineOccurrence, src, input string, mountSpace bool, needs []string) (string, error) {
	texts, err := g.postScript(ctx, hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey, Text: src}, DueAt: o.DueAt, Input: input, MountSpace: mountSpace, Needs: needs})
	if err != nil {
		return "", err
	}
	if len(texts) != 1 {
		return "", errors.New("probe script must emit exactly one value")
	}
	return texts[0], nil
}

func (g *Gateway) runIsolatedScript(ctx context.Context, o RoutineOccurrence) ([]string, error) {
	return g.postScript(ctx, hubruntime.ScriptRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: o.Job.Envelope, JobID: o.Job.ID, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, Trigger: o.Job.Trigger, IdempotencyKey: o.Job.IdempotencyKey, Text: o.Job.Text}, DueAt: o.DueAt})
}

func (g *Gateway) postScript(ctx context.Context, request hubruntime.ScriptRequest) ([]string, error) {
	if !g.config.Supervised || g.config.RuntimeURL == "" {
		return nil, errors.New("isolated script supervisor is unavailable")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.config.RuntimeURL, "/")+"/v1/script", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.config.RuntimeAuth)
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("isolated script failed")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(data) > 128*1024 {
		return nil, errors.New("script response exceeds limit")
	}
	var texts []string
	if json.Unmarshal(data, &texts) != nil || len(texts) > routines.ScriptMaxEmits {
		return nil, errors.New("invalid script response")
	}
	for _, text := range texts {
		if len(text) > routines.ScriptMaxEmitBytes {
			return nil, errors.New("script emit exceeds limit")
		}
	}
	return texts, nil
}
