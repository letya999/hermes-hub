package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

type generatedImage struct {
	bytes  []byte
	remote string
}

func apiBase(cfg hermesConfig) (string, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Model.BaseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("image generation endpoint is not configured")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func imageResponseFormat(delivery string) string {
	if delivery == DeliveryURL {
		return "url"
	}
	return "b64_json"
}

func (s *Session) cliproxyGenerate(ctx context.Context, base, key, model, prompt, delivery string) (generatedImage, error) {
	payload := map[string]any{
		"model":           model,
		"prompt":          prompt,
		"n":               1,
		"size":            CLIProxyImageSize,
		"response_format": imageResponseFormat(delivery),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return generatedImage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/images/generations", bytes.NewReader(raw))
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return s.readGeneratedImage(req)
}

func (s *Session) cliproxyEdit(ctx context.Context, base, key, model, prompt, delivery string, source []byte, filename string) (generatedImage, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	fields := [][2]string{
		{"model", model},
		{"prompt", prompt},
		{"n", "1"},
		{"size", CLIProxyImageSize},
		{"response_format", imageResponseFormat(delivery)},
	}
	for _, field := range fields {
		if err := form.WriteField(field[0], field[1]); err != nil {
			return generatedImage{}, err
		}
	}
	part, err := form.CreateFormFile("image", filename)
	if err != nil {
		return generatedImage{}, err
	}
	if _, err = part.Write(source); err != nil {
		return generatedImage{}, err
	}
	if err = form.Close(); err != nil {
		return generatedImage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/images/edits", &body)
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	return s.readGeneratedImage(req)
}

func (s *Session) cliproxyChatImage(ctx context.Context, base, key, model, prompt string, source []byte, mime string) (generatedImage, error) {
	var content any = prompt
	if len(source) > 0 {
		content = []any{
			map[string]any{"type": "text", "text": prompt},
			map[string]any{"type": "image_url", "image_url": map[string]string{
				"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(source),
			}},
		}
	}
	payload := map[string]any{
		"model": model,
		"messages": []any{map[string]any{
			"role":    "user",
			"content": content,
		}},
		"modalities":   []string{"image", "text"},
		"image_config": map[string]string{"aspect_ratio": CLIProxyChatAspect},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return generatedImage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return generatedImage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return s.readChatImage(req)
}

func (s *Session) readGeneratedImage(req *http.Request) (generatedImage, error) {
	if s.HTTP == nil {
		return generatedImage{}, errors.New("http client is not configured")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return generatedImage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return generatedImage{}, fmt.Errorf("image generation HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Data) != 1 {
		return generatedImage{}, errors.New("image generation returned no image")
	}
	item := parsed.Data[0]
	out := generatedImage{remote: strings.TrimSpace(item.URL)}
	if strings.TrimSpace(item.B64JSON) != "" {
		decoded, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil || len(decoded) == 0 {
			return generatedImage{}, errors.New("image generation returned no image")
		}
		out.bytes = decoded
	}
	if len(out.bytes) == 0 && out.remote == "" {
		return generatedImage{}, errors.New("image generation returned no image")
	}
	return out, nil
}

func (s *Session) readChatImage(req *http.Request) (generatedImage, error) {
	if s.HTTP == nil {
		return generatedImage{}, errors.New("http client is not configured")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return generatedImage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return generatedImage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return generatedImage{}, fmt.Errorf("image generation HTTP %d", resp.StatusCode)
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
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) != 1 {
		return generatedImage{}, errors.New("image generation returned no image")
	}
	images := parsed.Choices[0].Message.Images
	if len(images) == 0 {
		return generatedImage{}, errors.New("image generation returned no image")
	}
	if len(images) != 1 {
		return generatedImage{}, errors.New("image generation returned more than one image")
	}
	return imageFromChatURL(images[0].ImageURL.URL)
}

func imageFromChatURL(raw string) (generatedImage, error) {
	raw = strings.TrimSpace(raw)
	if decoded, isData := decodeDataImage(raw); isData {
		if len(decoded) == 0 {
			return generatedImage{}, errors.New("image generation returned no image")
		}
		return generatedImage{bytes: decoded}, nil
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return generatedImage{remote: raw}, nil
	}
	return generatedImage{}, errors.New("image generation returned no image")
}

func decodeDataImage(raw string) ([]byte, bool) {
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil, false
	}
	comma := strings.Index(raw, ",")
	if comma < 0 || !strings.Contains(strings.ToLower(raw[:comma]), ";base64") {
		return nil, true
	}
	payload := raw[comma+1:]
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil || len(decoded) == 0 {
		return nil, true
	}
	return decoded, true
}
