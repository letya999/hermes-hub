package mediasvc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GenEngine produces media bytes synchronously. hub-media runs one engine;
// the runtime-side tools never see provider credentials — only this service's
// bearer and whatever model ids the operator allowlisted.
type GenEngine interface {
	Generate(ctx context.Context, req GenRequest) (GenResult, error)
	Ready(ctx context.Context) bool
}

// AsyncEngine is an engine backed by a provider queue for long generations
// (video). Submit returns a provider handle the job persists; Poll resumes
// from that handle so a restart never pays for a second submission.
type AsyncEngine interface {
	Submit(ctx context.Context, req GenRequest) (remoteID string, err error)
	Poll(ctx context.Context, remoteID, model string) (GenResult, bool, error)
}

type GenRequest struct {
	Kind       string // "image" | "video"
	Model      string
	Prompt     string
	Size       string // provider-native size hint; empty = engine default
	Source     []byte // edit input bytes
	SourceName string
	SourceMime string
}

type GenResult struct {
	Data []byte
	Mime string
}

func genEngine(c Config) (GenEngine, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resolve, err := c.mediaKeyResolver()
	if err != nil {
		return nil, err
	}
	switch c.Engine {
	case "remote":
		if resolve == nil {
			resolve = staticKey(firstNonEmpty(c.UpstreamKey, envOr("OPENAI_API_KEY", "")))
		}
		return &remoteGen{base: c.Upstream, keyFor: resolve, chatModels: toSet(c.ChatModels), client: client}, nil
	case "fal":
		if resolve == nil {
			resolve = staticKey(firstNonEmpty(c.UpstreamKey, envOr("FAL_KEY", "")))
		}
		queue := c.QueueUpstream
		if queue == "" {
			queue = falQueueBase(c.Upstream)
		}
		return &falGen{base: c.Upstream, queue: queue, keyFor: resolve, client: client}, nil
	}
	return nil, fmt.Errorf("unknown media engine %q", c.Engine)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toSet(list []string) map[string]bool {
	out := map[string]bool{}
	for _, item := range list {
		out[strings.TrimSpace(item)] = true
	}
	return out
}

// falQueueBase derives the queue host from the sync base: fal.run requests go
// to queue.fal.run. Custom upstreams must set HUB_MEDIA_QUEUE_UPSTREAM.
func falQueueBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || !strings.HasSuffix(u.Hostname(), "fal.run") {
		return ""
	}
	u.Host = "queue." + u.Host
	return strings.TrimRight(u.String(), "/")
}

// ---- remote engine: OpenAI-compatible upstreams (cliproxy, a bespoke image
// service). Gemini-family ids route to /chat/completions — the allowlisted
// HUB_MEDIA_CHAT_MODELS set mirrors the runtime's chat-image model ids.

type remoteGen struct {
	base       string
	keyFor     keyResolver
	chatModels map[string]bool
	client     *http.Client
}

// apiKey resolves the call credential; nil resolver means the upstream needs
// no auth (e.g. a local cliproxy). Resolution errors fail the call closed.
func (e *remoteGen) apiKey(ctx context.Context) (string, error) {
	if e.keyFor == nil {
		return "", nil
	}
	return e.keyFor(ctx)
}

func (e *remoteGen) Generate(ctx context.Context, req GenRequest) (GenResult, error) {
	key, err := e.apiKey(ctx)
	if err != nil {
		return GenResult{}, err
	}
	if e.chatModels[req.Model] {
		return e.chatImage(ctx, req, key)
	}
	if len(req.Source) > 0 {
		return e.edit(ctx, req, key)
	}
	payload, err := json.Marshal(map[string]any{
		"model": req.Model, "prompt": req.Prompt, "n": 1,
		"size": orDefault(req.Size, "1024x1024"), "response_format": "b64_json",
	})
	if err != nil {
		return GenResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(e.base, "/images/generations"), bytes.NewReader(payload))
	if err != nil {
		return GenResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	e.authorize(httpReq, key)
	return e.readImages(httpReq)
}

func (e *remoteGen) edit(ctx context.Context, req GenRequest, key string) (GenResult, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, field := range [][2]string{
		{"model", req.Model}, {"prompt", req.Prompt}, {"n", "1"},
		{"size", orDefault(req.Size, "1024x1024")}, {"response_format", "b64_json"},
	} {
		if err := form.WriteField(field[0], field[1]); err != nil {
			return GenResult{}, err
		}
	}
	name := req.SourceName
	if name == "" {
		name = "image.png"
	}
	part, err := form.CreateFormFile("image", name)
	if err != nil {
		return GenResult{}, err
	}
	if _, err := part.Write(req.Source); err != nil {
		return GenResult{}, err
	}
	if err := form.Close(); err != nil {
		return GenResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(e.base, "/images/edits"), &body)
	if err != nil {
		return GenResult{}, err
	}
	httpReq.Header.Set("Content-Type", form.FormDataContentType())
	e.authorize(httpReq, key)
	return e.readImages(httpReq)
}

func (e *remoteGen) chatImage(ctx context.Context, req GenRequest, key string) (GenResult, error) {
	var content any = req.Prompt
	if len(req.Source) > 0 {
		mime := req.SourceMime
		if mime == "" {
			mime = "image/png"
		}
		content = []any{
			map[string]any{"type": "text", "text": req.Prompt},
			map[string]any{"type": "image_url", "image_url": map[string]string{
				"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(req.Source),
			}},
		}
	}
	payload, err := json.Marshal(map[string]any{
		"model": req.Model,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": content,
		}},
		"modalities":   []string{"image", "text"},
		"image_config": map[string]string{"aspect_ratio": orDefault(req.Size, "1:1")},
	})
	if err != nil {
		return GenResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL(e.base, "/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return GenResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	e.authorize(httpReq, key)
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return GenResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return GenResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return GenResult{}, fmt.Errorf("upstream image HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Images []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) != 1 || len(parsed.Choices[0].Message.Images) != 1 {
		return GenResult{}, errors.New("image generation returned no image")
	}
	data, mime, err := decodeDataImage(parsed.Choices[0].Message.Images[0].ImageURL.URL)
	if err != nil {
		return GenResult{}, err
	}
	return GenResult{Data: data, Mime: mime}, nil
}

func (e *remoteGen) authorize(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

func (e *remoteGen) readImages(req *http.Request) (GenResult, error) {
	resp, err := e.client.Do(req)
	if err != nil {
		return GenResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return GenResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return GenResult{}, fmt.Errorf("upstream image HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Data) != 1 || parsed.Data[0].B64JSON == "" {
		return GenResult{}, errors.New("image generation returned no image")
	}
	data, err := base64.StdEncoding.DecodeString(parsed.Data[0].B64JSON)
	if err != nil || len(data) == 0 {
		return GenResult{}, errors.New("image generation returned no image")
	}
	return GenResult{Data: data, Mime: sniffMime(data)}, nil
}

func (e *remoteGen) Ready(ctx context.Context) bool {
	key, err := e.apiKey(ctx)
	if err != nil {
		return false
	}
	return probe(ctx, e.client, apiURL(e.base, "/models"), key)
}

// ---- fal engine: sync models on fal.run, queue submissions on
// queue.fal.run. Result URLs land on fal's media CDN and are fetched through
// the service fetch allowlist (HUB_MEDIA_FETCH_HOSTS, e.g. fal.media).

type falGen struct {
	base   string
	queue  string
	keyFor keyResolver
	client *http.Client
	fetch  func(ctx context.Context, raw string) ([]byte, string, error)
}

// apiKey resolves the fal credential; fal has no anonymous path.
func (e *falGen) apiKey(ctx context.Context) (string, error) {
	if e.keyFor == nil {
		return "", errors.New("fal credential is not configured")
	}
	return e.keyFor(ctx)
}

func (e *falGen) Generate(ctx context.Context, req GenRequest) (GenResult, error) {
	key, err := e.apiKey(ctx)
	if err != nil {
		return GenResult{}, err
	}
	if len(req.Source) > 0 {
		return GenResult{}, errors.New("fal does not support image edit")
	}
	payload, err := json.Marshal(map[string]any{
		"prompt":                req.Prompt,
		"image_size":            orDefault(req.Size, "square_hd"),
		"num_inference_steps":   4,
		"output_format":         "png",
		"enable_safety_checker": false,
	})
	if err != nil {
		return GenResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.base, "/")+"/"+req.Model, bytes.NewReader(payload))
	if err != nil {
		return GenResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Key "+key)
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return GenResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return GenResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return GenResult{}, fmt.Errorf("fal image HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Images) != 1 || parsed.Images[0].URL == "" {
		return GenResult{}, errors.New("image generation returned no image")
	}
	return e.fetchResult(ctx, parsed.Images[0].URL)
}

func (e *falGen) fetchResult(ctx context.Context, raw string) (GenResult, error) {
	if e.fetch == nil {
		return GenResult{}, errors.New("remote result fetch is not configured")
	}
	data, mime, err := e.fetch(ctx, raw)
	if err != nil {
		return GenResult{}, err
	}
	return GenResult{Data: data, Mime: mime}, nil
}

func (e *falGen) Submit(ctx context.Context, req GenRequest) (string, error) {
	key, err := e.apiKey(ctx)
	if err != nil {
		return "", err
	}
	if e.queue == "" {
		return "", errors.New("queue upstream is not configured")
	}
	payload, err := json.Marshal(map[string]any{"prompt": req.Prompt})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.queue, "/")+"/"+req.Model, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Key "+key)
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fal queue submit HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.RequestID == "" {
		return "", errors.New("fal queue returned no request id")
	}
	return parsed.RequestID, nil
}

// Poll reads the queue status, then fetches the result document and finally
// the media bytes once. statuses: IN_QUEUE, IN_PROGRESS, COMPLETED — anything
// else is terminal.
func (e *falGen) Poll(ctx context.Context, remoteID, model string) (GenResult, bool, error) {
	key, err := e.apiKey(ctx)
	if err != nil {
		return GenResult{}, false, err
	}
	statusURL := strings.TrimRight(e.queue, "/") + "/" + model + "/requests/" + remoteID + "/status"
	var status struct {
		Status string `json:"status"`
	}
	if err := e.getJSON(ctx, statusURL, &status, key); err != nil {
		return GenResult{}, false, err
	}
	switch status.Status {
	case "IN_QUEUE", "IN_PROGRESS":
		return GenResult{}, false, nil
	case "COMPLETED":
	default:
		return GenResult{}, false, fmt.Errorf("fal queue status %q", status.Status)
	}
	resultURL := strings.TrimRight(e.queue, "/") + "/" + model + "/requests/" + remoteID
	var parsed struct {
		Video *struct {
			URL string `json:"url"`
		} `json:"video"`
		Videos []struct {
			URL string `json:"url"`
		} `json:"videos"`
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := e.getJSON(ctx, resultURL, &parsed, key); err != nil {
		return GenResult{}, false, err
	}
	remote := ""
	if parsed.Video != nil {
		remote = parsed.Video.URL
	}
	if remote == "" && len(parsed.Videos) == 1 {
		remote = parsed.Videos[0].URL
	}
	if remote == "" && len(parsed.Images) == 1 {
		remote = parsed.Images[0].URL
	}
	if remote == "" {
		return GenResult{}, false, errors.New("fal queue result has no media")
	}
	out, err := e.fetchResult(ctx, remote)
	if err != nil {
		return GenResult{}, false, err
	}
	return out, true, nil
}

func (e *falGen) getJSON(ctx context.Context, url string, out any, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Key "+key)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fal queue HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func (e *falGen) Ready(ctx context.Context) bool {
	// fal.run has no unauthenticated health endpoint; readiness is a
	// resolvable credential — a broker outage reads as unavailable.
	key, err := e.apiKey(ctx)
	return err == nil && key != ""
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// decodeDataImage accepts a data: URI (possibly without ;base64 marker is not
// supported — providers always send base64) and returns bytes + mime.
func decodeDataImage(raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil, "", errors.New("image generation returned no image")
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return nil, "", errors.New("image generation returned no image")
	}
	mime := strings.TrimPrefix(strings.ToLower(raw[:comma]), "data:")
	mime = strings.TrimSuffix(mime, ";base64")
	payload := raw[comma+1:]
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil || len(data) == 0 {
		return nil, "", errors.New("image generation returned no image")
	}
	if mime == "" {
		mime = sniffMime(data)
	}
	return data, mime, nil
}

// sniffMime maps the magic bytes the service accepts to a MIME type. Anything
// else is rejected — the service never trusts provider-supplied types.
func sniffMime(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		return "video/mp4"
	case len(data) >= 4 && bytes.Equal(data[0:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "video/webm"
	}
	return "application/octet-stream"
}

func mediaMimeOK(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "video/mp4", "video/webm", "video/quicktime":
		return true
	}
	return false
}

// fetchResult is the service-side media fetch: https only, host in the
// fetch allowlist, private/non-routable addresses refused at dial time.
// used by engines whose providers return URLs (fal images, fal queue video).
func fetchResult(ctx context.Context, raw string, hosts []string, limit int64) ([]byte, string, error) {
	if len(hosts) == 0 {
		return nil, "", errors.New("remote result fetch is not configured")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, "", errors.New("only https results are accepted")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", fmt.Errorf("host %q is not in the fetch allowlist", host)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	client := &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				h, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", h)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
						return nil, fmt.Errorf("refused private or non-routable address for %s", h)
					}
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("result fetch returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 || int64(len(data)) > limit {
		return nil, "", errors.New("result outside size bounds")
	}
	mime := resp.Header.Get("Content-Type")
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = mime[:idx]
	}
	mime = strings.ToLower(strings.TrimSpace(mime))
	if sniffed := sniffMime(data); mediaMimeOK(sniffed) {
		mime = sniffed
	}
	if !mediaMimeOK(mime) {
		return nil, "", fmt.Errorf("unsupported result type %q", mime)
	}
	return data, mime, nil
}
