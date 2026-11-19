package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/shared/locale"
)

const maxMCPBodyBytes = 4 << 20

// Hermes stamps each tool call with the run ID held in its per-turn context.
// The runtime resolves that ID to its admitted job before ToolHub verifies it.
func (s *runtimeHTTP) proxyMCP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeRuntimeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	s.proxyOnce.Do(func() {
		endpoint, err := url.Parse(os.Getenv("HUB_TOOLHUB_ENDPOINT"))
		if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || endpoint.Path != "/mcp" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
			s.proxyErr = api.ErrInvalid
			return
		}
		s.proxy, s.proxyErr = toolHubRelayHandler(endpoint.Scheme + "://" + endpoint.Host)
	})
	if s.proxyErr != nil {
		writeRuntimeError(w, http.StatusServiceUnavailable, "ToolHub route unavailable")
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path = "/mcp-run"
	r.Header.Del(api.HeaderJobID)
	r.Header.Del(api.HeaderRunID)
	r.Header.Del(contract.RunTokenHeader)
	r.Header.Del(contract.ModelServiceTokenHeader)
	r.Header.Del(contract.MemoryRunTokenHeader)
	r.Header.Del(contract.CommunicationRunTokenHeader)
	r.Header.Del(contract.EgressRunTokenHeader)
	r.Header.Del(RunDeliveryHeader)
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBodyBytes))
		if err != nil {
			writeRuntimeError(w, http.StatusRequestEntityTooLarge, "MCP request too large")
			return
		}
		var call struct {
			Method string `json:"method"`
			Params struct {
				Meta struct {
					RunID string `json:"x-hermes-hub-run-id"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &call) != nil {
			writeRuntimeError(w, http.StatusBadRequest, "invalid MCP request")
			return
		}
		if call.Method == "tools/call" {
			if !validHermesRunID(call.Params.Meta.RunID) {
				writeRuntimeError(w, http.StatusUnauthorized, "run identity missing")
				return
			}
			jobID, err := s.jobForRun(r.Context(), call.Params.Meta.RunID)
			if err != nil {
				writeRuntimeError(w, http.StatusUnauthorized, "run identity unavailable")
				return
			}
			token, err := s.runTokenFor(r.Context(), call.Params.Meta.RunID, contract.ToolHubAudience)
			if err != nil {
				lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
				writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
				return
			}
			if token != "" {
				r.Header.Set(contract.RunTokenHeader, token)
				for header, audience := range map[string]string{
					contract.MemoryRunTokenHeader:        contract.MemoryAudience,
					contract.CommunicationRunTokenHeader: contract.CommunicationAudience,
					contract.EgressRunTokenHeader:        contract.EgressAudience,
				} {
					delegated, err := s.runTokenFor(r.Context(), call.Params.Meta.RunID, audience)
					if err == nil && delegated != "" {
						r.Header.Set(header, delegated)
					}
				}
			}
			r.Header.Set(api.HeaderJobID, jobID)
			r.Header.Set(api.HeaderRunID, call.Params.Meta.RunID)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	s.metrics().mcpDelta(1)
	defer s.metrics().mcpDelta(-1)
	s.proxy.ServeHTTP(w, r)
}

func (s *runtimeHTTP) bindRun(jobID, runID string) error {
	return s.bindAuthorizedRun(jobID, runID, "")
}

func (s *runtimeHTTP) bindAuthorizedRun(jobID, runID, proof string) error {
	if jobID == "" || !validHermesRunID(runID) {
		return errors.New("invalid run binding")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.runJobs == nil {
		s.runJobs = map[string]string{}
		s.jobRuns = map[string]string{}
	}
	if existing := s.runJobs[runID]; existing != "" && existing != jobID {
		return errors.New("hermes run ownership changed during readmission")
	}
	if existing := s.jobRuns[jobID]; existing != "" && existing != runID {
		return errors.New("hermes run changed during readmission")
	}
	if proof != "" {
		if s.runAccess == nil {
			s.runAccess = make(map[string]runAccess)
		}
		access := s.runAccess[runID]
		if access.proof != "" && access.proof != proof {
			return errors.New("run delivery changed during readmission")
		}
		access.proof = proof
		s.runAccess[runID] = access
	}
	s.runJobs[runID], s.jobRuns[jobID] = jobID, runID
	s.signalRunChangeLocked()
	return nil
}

func (s *runtimeHTTP) beginAdmission() {
	s.runMu.Lock()
	s.pending++
	s.runMu.Unlock()
}

func (s *runtimeHTTP) finishAdmission() {
	s.runMu.Lock()
	s.pending--
	s.signalRunChangeLocked()
	s.runMu.Unlock()
}

func (s *runtimeHTTP) jobForRun(ctx context.Context, runID string) (string, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.runMu.Lock()
		jobID := s.runJobs[runID]
		if jobID != "" {
			s.runMu.Unlock()
			return jobID, nil
		}
		if s.pending == 0 {
			s.runMu.Unlock()
			return "", errors.New("run admission unavailable")
		}
		if s.runChange == nil {
			s.runChange = make(chan struct{})
		}
		changed := s.runChange
		s.runMu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", errors.New("run admission unavailable")
		case <-changed:
		}
	}
}

func (s *runtimeHTTP) releaseRun(jobID, runID, status string) {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
	default:
		return
	}
	s.runMu.Lock()
	if s.runJobs[runID] == jobID && s.jobRuns[jobID] == runID {
		delete(s.runJobs, runID)
		delete(s.jobRuns, jobID)
		delete(s.runAccess, runID)
		s.signalRunChangeLocked()
	}
	s.runMu.Unlock()
}

func (s *runtimeHTTP) signalRunChangeLocked() {
	if s.runChange != nil {
		close(s.runChange)
		s.runChange = nil
	}
}
