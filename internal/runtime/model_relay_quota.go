package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/locale"
)

// Provider quota is measured through the pinned CLIProxyAPI management API:
// auth-files selects the OAuth credential, then api-call makes an
// authenticated upstream request with $TOKEN$ substitution — the OAuth token
// never leaves the proxy. The management key arrives with the space's
// model-relay env file (HUB_CLIPROXY_MGMT_*); the gateway only sees the
// rendered lines through the admin-authenticated /v1/quota endpoint.
func (r *relayRegistry) serveQuota(w http.ResponseWriter, req *http.Request) {
	if r.admin == "" || req.Header.Get("Authorization") != "Bearer "+r.admin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_CLIPROXY_MGMT_URL")), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_CLIPROXY")), "/")
	}
	key := strings.TrimSpace(os.Getenv("HUB_CLIPROXY_MGMT_KEY"))
	lang := locale.LangOf(os.Getenv("HUB_WORKSPACE"))
	var lines []string
	if base == "" || key == "" {
		lines = []string{locale.T(lang, "quota.unconfigured")}
	} else if fetched, err := fetchQuotaLines(req.Context(), base, key, strings.TrimSpace(os.Getenv("HUB_CLIPROXY_AUTH_INDEX")), lang); err != nil {
		lines = []string{locale.T(lang, "quota.unavailable", err.Error())}
	} else {
		lines = fetched
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"lines": lines})
}

type cliproxyAuthFile struct {
	Provider  string `json:"provider"`
	AuthIndex string `json:"auth_index"`
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`
	Disabled  bool   `json:"disabled"`
}

// providerQuotaExhausted measures the hop's session quota through the
// instance's own management API (same listener as its endpoint) and reports
// whether any declared window already spent beyond its limit. Telemetry the
// session cannot report — a non-OAuth credential, an unparsable summary —
// is an error so the caller parks the hop instead of drifting unbounded.
func (h *modelHop) providerQuotaExhausted(ctx context.Context) (bool, error) {
	u, err := url.Parse(h.base)
	if err != nil {
		return false, err
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	summary, err := fetchSessionQuota(ctx, strings.TrimRight(u.String(), "/"),
		strings.TrimSpace(os.Getenv("HUB_CLIPROXY_MGMT_KEY")), "")
	if err != nil {
		return false, err
	}
	// {amount, unit: percent, per: 1d} means stop once the matching window
	// spent amount% of its quota — remainingFraction below the headroom.
	for _, l := range h.quota {
		threshold := 1.0 - float64(l.Amount)/100.0
		for _, g := range summary.Groups {
			for _, b := range g.Buckets {
				if b.Window == l.Per && b.RemainingFraction <= threshold {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// fetchSessionQuota reads the instance's auth-file list, picks its session
// credential and asks the proxy to call the provider's quota endpoint with
// the session's own token ($TOKEN$ never leaves the instance).
func fetchSessionQuota(ctx context.Context, base, key, wantIndex string) (quotaSummary, error) {
	var summary quotaSummary
	client := &http.Client{Timeout: 15 * time.Second}
	var files struct {
		Files []cliproxyAuthFile `json:"files"`
	}
	if err := managementCall(ctx, client, base, key, http.MethodGet, "/v0/management/auth-files", nil, &files); err != nil {
		return summary, err
	}
	file, found := pickAuthFile(files.Files, wantIndex)
	if !found {
		return summary, errors.New("no quota-reporting session")
	}
	payload := map[string]any{
		"auth_index": file.AuthIndex,
		"method":     http.MethodPost,
		"url":        "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"header":     map[string]string{"Content-Type": "application/json", "User-Agent": "antigravity", "Authorization": "Bearer $TOKEN$"},
	}
	if file.ProjectID != "" {
		data, _ := json.Marshal(map[string]string{"project": file.ProjectID})
		payload["data"] = string(data)
	}
	var call struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if err := managementCall(ctx, client, base, key, http.MethodPost, "/v0/management/api-call", payload, &call); err != nil {
		return summary, err
	}
	if call.StatusCode != http.StatusOK {
		return summary, fmt.Errorf("quota upstream HTTP %d", call.StatusCode)
	}
	if err := json.Unmarshal([]byte(call.Body), &summary); err != nil || len(summary.Groups) == 0 {
		return summary, errors.New("session reports no quota windows")
	}
	return summary, nil
}

// quotaBucket is one provider-reported window inside a session's quota
// summary — the only telemetry a provider_quota limit may measure against.
type quotaBucket struct {
	Window            string  `json:"window"`
	ResetTime         string  `json:"resetTime"`
	RemainingFraction float64 `json:"remainingFraction"`
}

type quotaSummary struct {
	Groups []struct {
		DisplayName string        `json:"displayName"`
		Buckets     []quotaBucket `json:"buckets"`
	} `json:"groups"`
}

func fetchQuotaLines(ctx context.Context, base, key, wantIndex string, lang locale.Lang) ([]string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var files struct {
		Files []cliproxyAuthFile `json:"files"`
	}
	if err := managementCall(ctx, client, base, key, http.MethodGet, "/v0/management/auth-files", nil, &files); err != nil {
		return nil, err
	}
	file, found := pickAuthFile(files.Files, wantIndex)
	if !found {
		return nil, errors.New(locale.T(lang, "quota.no-account"))
	}
	payload := map[string]any{
		"auth_index": file.AuthIndex,
		"method":     http.MethodPost,
		"url":        "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
		"header":     map[string]string{"Content-Type": "application/json", "User-Agent": "antigravity", "Authorization": "Bearer $TOKEN$"},
	}
	if file.ProjectID != "" {
		data, _ := json.Marshal(map[string]string{"project": file.ProjectID})
		payload["data"] = string(data)
	}
	var call struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if err := managementCall(ctx, client, base, key, http.MethodPost, "/v0/management/api-call", payload, &call); err != nil {
		return nil, err
	}
	if call.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", locale.T(lang, "quota.upstream", call.StatusCode))
	}
	var summary struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				Window            string  `json:"window"`
				ResetTime         string  `json:"resetTime"`
				RemainingFraction float64 `json:"remainingFraction"`
			} `json:"buckets"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(call.Body), &summary); err != nil || len(summary.Groups) == 0 {
		return nil, errors.New(locale.T(lang, "quota.unparsed"))
	}
	label := file.Email
	if label == "" {
		label = file.Provider
	}
	lines := []string{locale.T(lang, "quota.title", label)}
	for _, group := range summary.Groups {
		parts := []string{}
		for _, bucket := range group.Buckets {
			parts = append(parts, bucket.Window+" "+strconv.FormatFloat(bucket.RemainingFraction*100, 'f', 1, 64)+"%"+quotaResetSuffix(lang, bucket.ResetTime))
		}
		lines = append(lines, "  "+group.DisplayName+": "+strings.Join(parts, ", ")+".")
	}
	return lines, nil
}

// pickAuthFile uses only Antigravity credentials for the Antigravity quota API.
func pickAuthFile(files []cliproxyAuthFile, wantIndex string) (cliproxyAuthFile, bool) {
	for _, file := range files {
		if file.Disabled || file.AuthIndex == "" || file.Provider != "antigravity" {
			continue
		}
		if wantIndex != "" {
			if file.AuthIndex == wantIndex {
				return file, true
			}
			continue
		}
		return file, true
	}
	return cliproxyAuthFile{}, false
}

func quotaResetSuffix(lang locale.Lang, at string) string {
	when, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return ""
	}
	return locale.T(lang, "quota.reset", when.UTC().Format("02.01 15:04"))
}

// managementCall is a JSON round-trip against the cliproxy management API with
// the management key in the Authorization header.
func managementCall(ctx context.Context, client *http.Client, base, key, method, path string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body) // #nosec G704 -- internal quota endpoint
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req) // #nosec G704 -- internal quota endpoint
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("management HTTP %d", response.StatusCode)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(raw, result)
}
