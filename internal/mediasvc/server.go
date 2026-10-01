package mediasvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// service holds one role's runtime: engine, job store and the work semaphore.
type service struct {
	cfg  Config
	jobs *jobStore
	stt  STTEngine
	tts  TTSEngine
	sem  chan struct{}
}

// Serve builds the role's engine and returns the HTTP handler.
func Serve(cfg Config) (http.Handler, error) {
	store, err := newJobStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &service{cfg: cfg, jobs: store, sem: make(chan struct{}, cfg.Workers)}
	switch cfg.Role {
	case RoleSTT:
		s.stt, err = sttEngine(cfg)
	case RoleTTS:
		s.tts, err = ttsEngine(cfg)
	}
	if err != nil {
		return nil, err
	}
	// Requeue jobs orphaned by a restart; a "running" record is retried once.
	for _, j := range store.list() {
		if j.Status == "queued" || j.Status == "running" {
			j.Status = "queued"
			_ = store.put(j)
			go s.runJob(context.Background(), j.ID)
		}
	}
	go func() {
		for range time.Tick(10 * time.Minute) {
			store.sweep(cfg.JobTTL)
		}
	}()
	return s.routes(), nil
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/v1/audio/transcriptions", s.transcriptions)
	mux.HandleFunc("/v1/audio/speech", s.speech)
	mux.HandleFunc("/v1/voices", s.voices)
	mux.HandleFunc("/v1/jobs", s.jobsRoot)
	mux.HandleFunc("/v1/jobs/", s.jobItem)
	return mux
}

func (s *service) healthz(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Role == RoleSTT && s.stt != nil && s.stt.Ready(r.Context()) ||
		s.cfg.Role == RoleTTS && s.tts != nil && s.tts.Ready(r.Context()) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
}

func (s *service) authorized(r *http.Request) bool {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == s.cfg.Auth
}

func (s *service) transcriptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) || s.stt == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload)
	if err := r.ParseMultipartForm(s.cfg.MaxUpload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()
	dir, err := os.MkdirTemp(s.cfg.DataDir, "sync-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scratch dir failed")
		return
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "source"+strings.ToLower(filepath.Ext(header.Filename)))
	if err := saveBounded(src, file, s.cfg.MaxUpload); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	wav := filepath.Join(dir, "audio.wav")
	if err := decodeAudioFn(r.Context(), src, wav); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	opts := TranscribeOpts{Language: r.FormValue("language"), Model: r.FormValue("model")}
	segments, err := s.transcribeWav(r.Context(), wav, opts)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	format := r.FormValue("response_format")
	text := transcriptText(segments)
	switch format {
	case "srt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, toSRT(segments))
	case "verbose_json":
		writeJSON(w, http.StatusOK, map[string]any{"text": text, "segments": segments})
	case "text", "":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, text)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	}
}

// speech is the OpenAI-compatible TTS endpoint: {model, voice, input,
// response_format} in, audio bytes out. Wav is canonical; "opus" requests are
// transcoded to ogg/libopus for Telegram voice bubbles.
func (s *service) speech(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authorized(r) || s.tts == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Input  string `json:"input"`
		Voice  string `json:"voice"`
		Format string `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		writeError(w, http.StatusBadRequest, "invalid speech request")
		return
	}
	audio, err := s.tts.Synthesize(r.Context(), req.Input, req.Voice)
	if err != nil || len(audio) == 0 {
		writeError(w, http.StatusBadGateway, "synthesis failed")
		return
	}
	if req.Format == "opus" || req.Format == "ogg" {
		dir, err := os.MkdirTemp(s.cfg.DataDir, "tts-*")
		if err == nil {
			defer os.RemoveAll(dir)
			src := filepath.Join(dir, "in.wav")
			dst := filepath.Join(dir, "out.ogg")
			if os.WriteFile(src, audio, 0600) == nil {
				enc := exec.CommandContext(r.Context(), "ffmpeg", "-nostdin", "-y", "-i", src, "-c:a", "libopus", "-b:a", "32k", dst)
				if enc.Run() == nil {
					if b, err := os.ReadFile(dst); err == nil {
						audio, req.Format = b, "ogg"
					}
				}
			}
		}
	}
	if req.Format == "ogg" {
		w.Header().Set("Content-Type", "audio/ogg")
	} else {
		w.Header().Set("Content-Type", "audio/wav")
	}
	_, _ = w.Write(audio)
}

func (s *service) voices(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) || s.tts == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"voices": []string{s.cfg.Voice}, "default": s.cfg.Voice})
}

// jobRequest is the async API body; SourceURL/SourcePath are materialized
// inside the job so slow fetches never hold the request open.
type jobRequest struct {
	SourceURL  string `json:"source_url"`
	SourcePath string `json:"source_path"`
	Language   string `json:"language"`
	Model      string `json:"model"`
	Diarize    bool   `json:"diarize"`
}

func (s *service) jobsRoot(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) || s.stt == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch r.Method {
	case http.MethodGet:
		var out []map[string]any
		for _, j := range s.jobs.list() {
			out = append(out, map[string]any{"id": j.ID, "status": j.Status, "created_at": j.CreatedAt, "error": j.Error})
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
	case http.MethodPost:
		// Two ingest forms: JSON {source_url|source_path} or multipart upload.
		var req jobRequest
		var upload []byte
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxSource)
			if err := r.ParseMultipartForm(s.cfg.MaxSource); err != nil {
				writeError(w, http.StatusRequestEntityTooLarge, "upload too large")
				return
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				writeError(w, http.StatusBadRequest, "file field is required")
				return
			}
			defer file.Close()
			upload, err = io.ReadAll(io.LimitReader(file, s.cfg.MaxSource+1))
			if err != nil || int64(len(upload)) > s.cfg.MaxSource {
				writeError(w, http.StatusRequestEntityTooLarge, "upload too large")
				return
			}
			req.Language = r.FormValue("language")
			req.Model = r.FormValue("model")
			req.Diarize = r.FormValue("diarize") == "true"
		} else {
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid job request")
				return
			}
		}
		if upload == nil && req.SourceURL == "" && req.SourcePath == "" {
			writeError(w, http.StatusBadRequest, "a source is required")
			return
		}
		j := &Job{ID: newJobID(), Status: "queued", CreatedAt: time.Now().UTC(),
			Language: req.Language, Model: req.Model, Diarize: req.Diarize,
			Bytes: int64(len(upload))}
		if err := s.jobs.put(j); err != nil {
			writeError(w, http.StatusInternalServerError, "job persist failed")
			return
		}
		dir := s.jobs.dirOf(j.ID)
		if b, _ := json.Marshal(req); len(b) > 0 {
			_ = os.WriteFile(filepath.Join(dir, "request.json"), b, 0600)
		}
		if upload != nil {
			if err := os.WriteFile(filepath.Join(dir, "upload.bin"), upload, 0600); err != nil {
				writeError(w, http.StatusInternalServerError, "upload persist failed")
				return
			}
		}
		go s.runJob(context.Background(), j.ID)
		writeJSON(w, http.StatusAccepted, map[string]any{"id": j.ID, "status": j.Status})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *service) jobItem(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) || s.stt == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, file, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), "/")
	j, err := s.jobs.get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	switch {
	case file == "" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"id": j.ID, "status": j.Status, "text": j.Text, "error": j.Error, "language": j.Language, "diarize": j.Diarize})
	case file == "result" && r.Method == http.MethodGet:
		if j.Status != "done" {
			writeError(w, http.StatusConflict, "job is not finished")
			return
		}
		format := r.URL.Query().Get("format")
		name := "result.txt"
		if format == "json" {
			name = "result.json"
		} else if format == "srt" {
			name = "result.srt"
		}
		http.ServeFile(w, r, filepath.Join(s.jobs.dirOf(j.ID), name))
	case file == "" && r.Method == http.MethodDelete:
		if j.Status == "running" {
			writeError(w, http.StatusConflict, "job is running")
			return
		}
		_ = os.RemoveAll(s.jobs.dirOf(j.ID))
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func saveBounded(dst string, src io.Reader, limit int64) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("body too large")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}
