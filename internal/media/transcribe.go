package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"
)

// Transcribe uploads only bytes read through the admitted workspace root.
func (s *Session) Transcribe(ctx context.Context, rel, language string, timestamps bool) (map[string]any, error) {
	base := strings.TrimRight(getenvTrim("HUB_STT_URL"), "/")
	if base == "" || s.HTTP == nil {
		return nil, errors.New("speech transcription service is not configured")
	}
	if language != "" && (len(language) != 2 || language[0] < 'a' || language[0] > 'z' || language[1] < 'a' || language[1] > 'z') {
		return nil, errors.New("language must be a two-letter code")
	}
	audio, err := s.readRegular(rel, 20<<20)
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 {
		return nil, errors.New("audio file is empty")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", path.Base(rel))
	if err != nil {
		return nil, err
	}
	_, _ = part.Write(audio)
	_ = form.WriteField("language", language)
	_ = form.WriteField("response_format", "verbose_json")
	if err := form.Close(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/audio/transcriptions", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if auth := getenvTrim("HUB_MEDIA_AUTH"); auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	raw, _, err := s.serviceDo(req, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	var result struct {
		Text     string            `json:"text"`
		Segments []json.RawMessage `json:"segments"`
	}
	if json.Unmarshal(raw, &result) != nil || strings.TrimSpace(result.Text) == "" {
		return nil, errors.New("transcription service returned no transcript")
	}
	out := map[string]any{"text": result.Text, "path": rel}
	if timestamps {
		out["segments"] = result.Segments
	}
	return out, nil
}
