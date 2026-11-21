package hermes

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/shared/locale"
)

type runDeliveryKey struct{}
type runAccess struct {
	proof   string
	tokens  map[string]string
	session string
}

func runDeliveryFrom(r *http.Request) string {
	if len(r.Header.Values(RunDeliveryHeader)) != 1 {
		return ""
	}
	proof := r.Header.Get(RunDeliveryHeader)
	if len(proof) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(proof); err != nil {
		return ""
	}
	return proof
}

func withRunDelivery(r *http.Request) context.Context {
	return context.WithValue(r.Context(), runDeliveryKey{}, runDeliveryFrom(r))
}

func (s *runtimeHTTP) installRunAuthorization(w http.ResponseWriter, r *http.Request) {
	lang, _ := locale.Negotiate(r.Header.Get("Accept-Language"))
	proof := runDeliveryFrom(r)
	if r.Method != http.MethodPut || !s.authorized(r) || proof == "" || r.Header.Get("Origin") != "" {
		writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
		return
	}
	var grant RunAuthorization
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 45<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&grant) != nil || decoder.Decode(new(any)) != io.EOF || !validHermesRunID(grant.RunID) ||
		grant.JobID == "" || grant.Generation == "" || grant.Generation != os.Getenv("HUB_RUNTIME_GENERATION") ||
		len(grant.Tokens) == 0 || len(grant.Tokens) > 5 {
		writeRuntimeError(w, http.StatusBadRequest, locale.T(lang, "run.authorization-invalid"))
		return
	}
	for audience, token := range grant.Tokens {
		switch audience {
		case contract.ToolHubAudience, contract.MemoryAudience, contract.ModelAudience, contract.EgressAudience, contract.CommunicationAudience:
		default:
			writeRuntimeError(w, http.StatusBadRequest, locale.T(lang, "run.authorization-invalid"))
			return
		}
		if len(token) == 0 || len(token) > 8192 {
			writeRuntimeError(w, http.StatusBadRequest, locale.T(lang, "run.authorization-invalid"))
			return
		}
	}
	s.runMu.Lock()
	access := s.runAccess[grant.RunID]
	match := s.runJobs[grant.RunID] == grant.JobID && subtle.ConstantTimeCompare([]byte(proof), []byte(access.proof)) == 1
	if match {
		access.tokens = grant.Tokens
		s.runAccess[grant.RunID] = access
		s.signalRunChangeLocked()
	}
	s.runMu.Unlock()
	if !match {
		writeRuntimeError(w, http.StatusUnauthorized, locale.T(lang, "run.authorization-denied"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *runtimeHTTP) runTokenFor(ctx context.Context, runID, audience string) (string, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		s.runMu.Lock()
		_, bound := s.runJobs[runID]
		access := s.runAccess[runID]
		if !bound {
			s.runMu.Unlock()
			return "", errors.New("run authorization unavailable")
		}
		if access.proof == "" {
			s.runMu.Unlock()
			return "", nil
		}
		if access.tokens != nil {
			token := access.tokens[audience]
			s.runMu.Unlock()
			if token == "" {
				return "", errors.New("run authorization unavailable")
			}
			return token, nil
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
			return "", errors.New("run authorization unavailable")
		case <-changed:
		}
	}
}
