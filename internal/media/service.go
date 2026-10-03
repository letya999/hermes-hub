package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// The hub-media sidecar carries provider credentials so runtimes never see
// them. When HUB_MEDIA_URL is set, generation and edit calls go to the
// service over the internal network; the service streams back raw media bytes
// which land in the artifact buckets exactly like provider output.
// Unset keeps the direct provider path for non-stack runs.

// MaxVideoBytes caps fetched video artifacts under Telegram's 50 MB upload
// bound, with headroom for multipart framing.
const MaxVideoBytes = 48 << 20

// VideoFetchTimeout covers the status check plus one bounded result download.
const VideoFetchTimeout = 2 * time.Minute

func (s *Session) mediaService() (base, auth string) {
	return strings.TrimRight(getenvTrim("HUB_MEDIA_URL"), "/"), getenvTrim("HUB_MEDIA_AUTH")
}

func (s *Session) serviceGenerate(ctx context.Context, base, auth, name, model, prompt, sourceName string, source []byte) (map[string]any, error) {
	var req *http.Request
	var err error
	if len(source) > 0 {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("model", model); err != nil {
			return nil, err
		}
		if err := form.WriteField("prompt", prompt); err != nil {
			return nil, err
		}
		part, err := form.CreateFormFile("image", sourceName)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(source); err != nil {
			return nil, err
		}
		if err := form.Close(); err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/images/edits", &body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", form.FormDataContentType())
	} else {
		payload, err := json.Marshal(map[string]any{"model": model, "prompt": prompt})
		if err != nil {
			return nil, err
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/images/generations", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	body, mime, err := s.serviceDo(req, MaxImageBytes)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, errors.New("media service returned a non-image payload")
	}
	format, err := sniffImage(body)
	if err != nil {
		return nil, err
	}
	width, height, err := imageBounds(body)
	if err != nil {
		return nil, err
	}
	rel := path.Join("artifacts", "images", name+"."+imageExt(format))
	if err = s.writeNew(rel, body); err != nil {
		return nil, err
	}
	return map[string]any{
		"path": rel, "format": format, "width": width, "height": height, "bytes": len(body),
		"model": model, "provider": "hub-media", "delivery": DeliveryWorkspace,
		"secret_values_included": false,
	}, nil
}

// VideoGenerate submits one async video job to hub-media and returns the job
// id immediately. The agent reports the id and later calls media_fetch.
func (s *Session) VideoGenerate(ctx context.Context, prompt string) (map[string]any, error) {
	if err := s.gen.enter(); err != nil {
		return nil, err
	}
	defer s.gen.leave()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, _, err := s.grantedImage(); err != nil {
		return nil, err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || utf8.RuneCountInString(prompt) > MaxPromptRunes || strings.Contains(prompt, "\x00") {
		return nil, errors.New("prompt must be a short non-empty string")
	}
	if containsSecret(prompt) {
		return nil, errors.New("refusing to send credential material")
	}
	base, auth := s.mediaService()
	if base == "" || s.HTTP == nil {
		return nil, errors.New("media service is not configured")
	}
	payload, err := json.Marshal(map[string]any{"kind": "video", "prompt": prompt})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/jobs", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("media service job submit HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.ID == "" {
		return nil, errors.New("media service returned no job id")
	}
	return map[string]any{
		"job_id": parsed.ID, "status": parsed.Status, "kind": "video",
		"secret_values_included": false,
	}, nil
}

// MediaFetch checks one media job and, when finished, stages the result under
// artifacts/videos so the channel delivery picks it up like any artifact.
func (s *Session) MediaFetch(ctx context.Context, jobID, name string) (map[string]any, error) {
	if err := s.gen.enter(); err != nil {
		return nil, err
	}
	defer s.gen.leave()
	ctx, cancel := context.WithTimeout(ctx, VideoFetchTimeout)
	defer cancel()
	base, auth := s.mediaService()
	if base == "" || s.HTTP == nil {
		return nil, errors.New("media service is not configured")
	}
	if !validJobID(jobID) {
		return nil, errors.New("invalid job id")
	}
	if !validName(name) {
		return nil, errors.New("media name must be a short identifier")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("job not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("media service job HTTP %d", resp.StatusCode)
	}
	var status struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, err
	}
	switch status.Status {
	case "queued", "running":
		return map[string]any{"job_id": jobID, "status": status.Status, "secret_values_included": false}, nil
	case "failed":
		return nil, fmt.Errorf("media job failed: %s", status.Error)
	case "done":
	default:
		return nil, fmt.Errorf("media job status %q", status.Status)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/jobs/"+jobID+"/result", nil)
	if err != nil {
		return nil, err
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	data, mime, err := s.serviceDo(req, MaxVideoBytes)
	if err != nil {
		return nil, err
	}
	ext := mediaExt(mime)
	if ext == "" {
		return nil, fmt.Errorf("unsupported media type %q", mime)
	}
	bucket := "documents"
	if strings.HasPrefix(mime, "image/") {
		bucket = "images"
	} else if strings.HasPrefix(mime, "video/") {
		bucket = "videos"
	}
	rel := path.Join("artifacts", bucket, name+"."+ext)
	if err = s.writeNew(rel, data); err != nil {
		return nil, err
	}
	return map[string]any{
		"job_id": jobID, "status": "done", "path": rel, "mime": mime, "bytes": len(data),
		"secret_values_included": false,
	}, nil
}

// serviceDo reads a bounded response body and returns the content type the
// service stamped on it.
func (s *Session) serviceDo(req *http.Request, limit int) ([]byte, string, error) {
	if s.HTTP == nil {
		return nil, "", errors.New("http client is not configured")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("media service HTTP %d", resp.StatusCode)
	}
	if len(body) == 0 || len(body) > limit {
		return nil, "", fmt.Errorf("media result outside size bounds")
	}
	mime := resp.Header.Get("Content-Type")
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = mime[:idx]
	}
	return body, strings.ToLower(strings.TrimSpace(mime)), nil
}

func validJobID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func mediaExt(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "video/quicktime":
		return "mov"
	}
	return ""
}
