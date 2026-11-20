package task

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/shared/httpapi"
)

const AssignmentAuthorityPath = "/v1/automation/verify-run/"

type AssignmentAuthority interface {
	AuthorizeAssignmentJob(ctx context.Context, jobID string) error
}

type assignmentHTTP struct{ httpapi.Client }

func NewAssignmentHTTPClient(baseURL, token string, hc *http.Client) AssignmentAuthority {
	return assignmentHTTP{httpapi.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (a assignmentHTTP) AuthorizeAssignmentJob(ctx context.Context, jobID string) error {
	if !ValidJobID(jobID) {
		return ErrInvalid
	}
	var result struct{ Allow bool }
	if err := a.Do(ctx, http.MethodGet, AssignmentAuthorityPath+url.PathEscape(jobID), nil, &result); err != nil {
		return err
	}
	if !result.Allow {
		return ErrForbidden
	}
	return nil
}

func AssignmentAuthorityHandler(owner AssignmentAuthority, token string) http.Handler {
	return httpapi.Handle(token, func(r *http.Request) (any, error) {
		jobID := strings.TrimPrefix(r.URL.Path, AssignmentAuthorityPath)
		if r.Method != http.MethodGet || r.Header.Get("Origin") != "" || !ValidJobID(jobID) {
			return nil, ErrForbidden
		}
		if err := owner.AuthorizeAssignmentJob(r.Context(), jobID); err != nil {
			return nil, err
		}
		return map[string]bool{"allow": true}, nil
	})
}
