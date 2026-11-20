package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/shared/auth"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

const AutomationServiceAssertionPath = "/v1/identity/automation-service-token"
const CommunicationServiceAuthEnv = "HUB_COMMUNICATION_SERVICE_AUTH"
const AutomationServiceAudience = "tool-hub-automation"
const MemoryContextServiceAudience = "tool-hub-memory-context"
const MemoryCaptureServiceAudience = "tool-hub-memory-capture"
const MemoryCaptureServiceAssertionPath = "/v1/identity/memory-capture-service-token"

// ServiceRequest limits one service bearer to the exact request bytes.
type ServiceRequest = auth.ServiceRequest

type ServiceClaims = auth.ServiceClaims

func VerifyServiceToken(token, audience, service string, request ServiceRequest, keys []PublicSigningKey, now time.Time) error {
	return auth.VerifyServiceToken(token, audience, service, request, keys, now)
}

func AutomationServiceCredential(serviceRoot string) string {
	return serviceCredential(serviceRoot, "hub-service-token/communication-automation/v1")
}

func MemoryCaptureServiceCredential(serviceRoot string) string {
	return serviceCredential(serviceRoot, "hub-service-token/communication-memory-capture/v1")
}

func serviceCredential(serviceRoot, scope string) string {
	if serviceRoot == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(serviceRoot))
	_, _ = io.WriteString(mac, scope)
	return hex.EncodeToString(mac.Sum(nil))
}

type AutomationServiceSigner interface {
	IssueAutomationService(context.Context, ServiceRequest) (string, error)
}

type MemoryCaptureServiceSigner interface {
	IssueMemoryCaptureService(context.Context, ServiceRequest) (string, error)
}

type automationServiceResponse struct {
	Token string `json:"token"`
}

type serviceTokenHTTP struct{ httpapi.Client }

func NewAutomationServiceSigner(baseURL, bootstrap string) AutomationServiceSigner {
	return newServiceTokenHTTP(baseURL, bootstrap)
}

func NewMemoryCaptureServiceSigner(baseURL, bootstrap string) MemoryCaptureServiceSigner {
	return newServiceTokenHTTP(baseURL, bootstrap)
}

func newServiceTokenHTTP(baseURL, bootstrap string) serviceTokenHTTP {
	hc := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return serviceTokenHTTP{httpapi.Client{BaseURL: baseURL, Token: bootstrap, HTTP: hc}}
}

func (c serviceTokenHTTP) IssueAutomationService(ctx context.Context, request ServiceRequest) (string, error) {
	return c.issue(ctx, AutomationServiceAssertionPath, request)
}

func (c serviceTokenHTTP) IssueMemoryCaptureService(ctx context.Context, request ServiceRequest) (string, error) {
	return c.issue(ctx, MemoryCaptureServiceAssertionPath, request)
}

func (c serviceTokenHTTP) issue(ctx context.Context, path string, request ServiceRequest) (string, error) {
	if !request.Valid() {
		return "", ErrInvalid
	}
	var result automationServiceResponse
	if err := c.Do(ctx, http.MethodPost, path, request, &result); err != nil {
		return "", err
	}
	if len(result.Token) > 4096 || strings.Count(result.Token, ".") != 2 {
		return "", ErrUnavailable
	}
	return result.Token, nil
}

func AutomationServiceHandler(signer AutomationServiceSigner, bootstrap, environment string) http.Handler {
	return serviceTokenHandler(signer.IssueAutomationService, bootstrap, environment)
}

func MemoryCaptureServiceHandler(signer MemoryCaptureServiceSigner, bootstrap, environment string) http.Handler {
	return serviceTokenHandler(signer.IssueMemoryCaptureService, bootstrap, environment)
}

func serviceTokenHandler(issue func(context.Context, ServiceRequest) (string, error), bootstrap, environment string) http.Handler {
	return httpapi.Handle(bootstrap, func(r *http.Request) (any, error) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != "" {
			return nil, ErrForbidden
		}
		var request ServiceRequest
		decode := json.NewDecoder(r.Body)
		decode.DisallowUnknownFields()
		if decode.Decode(&request) != nil || decode.Decode(new(any)) != io.EOF || request.Environment != environment || !request.Valid() {
			return nil, ErrInvalid
		}
		token, err := issue(r.Context(), request)
		return automationServiceResponse{Token: token}, err
	})
}
