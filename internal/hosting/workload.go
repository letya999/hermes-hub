package hosting

import (
	"context"
	"net/http"
	"time"
)

// Workload contains no credentials or caller-selected daemon paths.
type Workload struct {
	AdmissionDigest string    `json:"admission_digest,omitempty"`
	Binding         Binding   `json:"binding"`
	Name            string    `json:"name"`
	Revision        uint64    `json:"revision"`
	Release         string    `json:"release"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type Executor interface {
	Check(context.Context, Workload) error
	Ensure(context.Context, Workload) error
	Remove(context.Context, Workload) error
	RoundTrip(context.Context, Workload, *http.Request) (*http.Response, error)
}

func (s *Service) workload(r record) Workload {
	return Workload{Binding: s.Binding(), Name: r.Name, Revision: r.Revision, Release: r.Release, ExpiresAt: r.ExpiresAt, AdmissionDigest: r.AdmissionDigest}
}
