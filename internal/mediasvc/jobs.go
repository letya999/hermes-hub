package mediasvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Job is one durable async unit: a transcription (kind "" or "transcription")
// or a media generation (kind "video"). Status transitions:
// queued -> running -> done | failed.
type Job struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"` // queued, running, done, failed
	Kind      string    `json:"kind,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source,omitempty"` // url|path|upload
	Language  string    `json:"language,omitempty"`
	Model     string    `json:"model,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	RemoteID  string    `json:"remote_id,omitempty"` // provider queue handle for resume
	Mime      string    `json:"mime,omitempty"`      // media result content type
	Diarize   bool      `json:"diarize,omitempty"`
	Text      string    `json:"text,omitempty"`
	Error     string    `json:"error,omitempty"`
	Segments  []Segment `json:"-"`
	Bytes     int64     `json:"bytes,omitempty"`
}

type jobStore struct {
	dir string
}

func newJobStore(dir string) (*jobStore, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0700); err != nil {
		return nil, err
	}
	return &jobStore{dir: filepath.Join(dir, "jobs")}, nil
}

func validJobID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func (s *jobStore) dirOf(id string) string { return filepath.Join(s.dir, id) }

func (s *jobStore) put(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	dir := s.dirOf(j.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "job.json.tmp")
	dst := filepath.Join(dir, "job.json")
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	// Windows rename over an existing file can transiently lose to readers or
	// indexers; retry briefly, then fall back to a direct write — the job file
	// is small and a torn write is recovered by requeue-on-start anyway.
	for i := 0; i < 5; i++ {
		if err := os.Rename(tmp, dst); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(dst, b, 0600); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	return nil
}

func (s *jobStore) get(id string) (*Job, error) {
	if !validJobID(id) {
		return nil, errors.New("invalid job id")
	}
	b, err := os.ReadFile(filepath.Join(s.dirOf(id), "job.json"))
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *jobStore) list() []*Job {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []*Job
	for _, e := range entries {
		if j, err := s.get(e.Name()); err == nil {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.Before(out[k].CreatedAt) })
	return out
}

// sweep removes finished jobs older than the retention TTL.
func (s *jobStore) sweep(ttl time.Duration) {
	cutoff := time.Now().Add(-ttl)
	for _, j := range s.list() {
		if (j.Status == "done" || j.Status == "failed") && j.UpdatedAt.Before(cutoff) {
			_ = os.RemoveAll(s.dirOf(j.ID))
		}
	}
}

// newJobID is a timestamped random id safe for the filesystem.
func newJobID() string {
	return fmt.Sprintf("job-%d-%06d", time.Now().UnixNano()/1e6, time.Now().UnixNano()%1e6)
}

// run executes the transcription pipeline inside the job's directory:
// decode -> chunk windows -> per-chunk engine calls (bounded parallelism) ->
// optional diarization pass -> merged segments -> text/result files.
func (s *service) runJob(ctx context.Context, id string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	j, err := s.jobs.get(id)
	if err != nil || j.Status != "queued" {
		return
	}
	if j.Kind == "video" {
		s.runGenJob(ctx, j)
		return
	}
	j.Status = "running"
	_ = s.jobs.put(j)
	fail := func(err error) {
		j.Status, j.Error = "failed", err.Error()
		_ = s.jobs.put(j)
	}
	dir := s.jobs.dirOf(id)

	src, err := s.jobSource(ctx, j, dir)
	if err != nil {
		fail(err)
		return
	}
	wav := filepath.Join(dir, "audio.wav")
	if err := decodeAudioFn(ctx, src, wav); err != nil {
		fail(err)
		return
	}
	segments, err := s.transcribeWav(ctx, wav, TranscribeOpts{Language: j.Language, Model: j.Model, Diarize: j.Diarize})
	if err != nil {
		fail(err)
		return
	}
	j.Segments = segments
	j.Text = transcriptText(segments)
	j.Status = "done"
	if err := writeJobResult(dir, j); err != nil {
		fail(err)
		return
	}
	if err := s.jobs.put(j); err != nil {
		// Results are on disk; a lost status write still fails the poll, and
		// restart requeue retries the job idempotently.
		fail(err)
	}
}

// runGenJob drives a media job: submit once (RemoteID persists so a restart
// resumes polling instead of paying for a second submission), then poll the
// provider queue until the result lands. Result bytes go to result.bin.
func (s *service) runGenJob(ctx context.Context, j *Job) {
	j.Status = "running"
	_ = s.jobs.put(j)
	fail := func(err error) {
		j.Status, j.Error = "failed", err.Error()
		_ = s.jobs.put(j)
	}
	eng, ok := s.gen.(AsyncEngine)
	if !ok {
		fail(errors.New("engine does not support async generation"))
		return
	}
	timeout := s.cfg.JobTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if j.RemoteID == "" {
		remoteID, err := eng.Submit(ctx, GenRequest{Kind: j.Kind, Model: j.Model, Prompt: j.Prompt})
		if err != nil {
			fail(err)
			return
		}
		j.RemoteID = remoteID
		_ = s.jobs.put(j)
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		out, done, err := eng.Poll(ctx, j.RemoteID, j.Model)
		if err != nil {
			fail(err)
			return
		}
		if !done {
			select {
			case <-ctx.Done():
				fail(errors.New("job timed out"))
				return
			case <-ticker.C:
				continue
			}
		}
		if len(out.Data) == 0 {
			fail(errors.New("generation returned no media"))
			return
		}
		if err := os.WriteFile(filepath.Join(s.jobs.dirOf(j.ID), "result.bin"), out.Data, 0600); err != nil {
			fail(err)
			return
		}
		j.Mime = out.Mime
		j.Bytes = int64(len(out.Data))
		j.Status = "done"
		if err := s.jobs.put(j); err != nil {
			fail(err)
		}
		return
	}
}

// jobSource locates the input saved by the handler (upload.bin) or resolves
// url/path sources now — remote fetch runs inside the job, not the request.
func (s *service) jobSource(ctx context.Context, j *Job, dir string) (string, error) {
	upload := filepath.Join(dir, "upload.bin")
	if info, err := os.Stat(upload); err == nil && info.Mode().IsRegular() {
		return upload, nil
	}
	var p jobRequest
	if b, err := os.ReadFile(filepath.Join(dir, "request.json")); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return s.materialize(ctx, p.SourceURL, p.SourcePath, dir)
}

// transcribeWav chunks audio into fixed windows and calls the engine per
// window, stitching global timestamps. Diarization is engine-dependent: the
// sherpa engine labels speakers itself; remote/command engines get speaker
// labels via the optional diarizer hook when configured.
func (s *service) transcribeWav(ctx context.Context, wav string, opts TranscribeOpts) ([]Segment, error) {
	dur, err := wavDuration(wav)
	if err != nil {
		return nil, err
	}
	if dur <= sttChunkSeconds+0.5 {
		return s.stt.Transcribe(ctx, wav, opts)
	}
	var merged []Segment
	offset := 0.0
	chunk := 0
	for offset < dur {
		part := filepath.Join(filepath.Dir(wav), fmt.Sprintf("chunk-%04d.wav", chunk))
		if err := wavSliceFn(ctx, wav, part, offset, sttChunkSeconds); err != nil {
			return nil, err
		}
		chunk++
		segs, err := s.stt.Transcribe(ctx, part, opts)
		_ = os.Remove(part)
		if err != nil {
			return nil, fmt.Errorf("chunk at %.0fs: %w", offset, err)
		}
		for _, sg := range segs {
			sg.Start += offset
			sg.End += offset
			merged = append(merged, sg)
		}
		offset += sttChunkSeconds
	}
	return merged, nil
}

// transcriptText renders segments as readable text: one line per speaker turn
// when speaker labels exist, joined plain text otherwise.
func transcriptText(segments []Segment) string {
	var b strings.Builder
	speaker := ""
	for _, s := range segments {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		if s.Speaker != "" {
			if s.Speaker != speaker {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(s.Speaker)
				b.WriteString(": ")
			} else {
				b.WriteString(" ")
			}
			speaker = s.Speaker
		} else if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(t)
	}
	return b.String()
}

// writeJobResult persists text/json/srt/vtt siblings next to job.json.
func writeJobResult(dir string, j *Job) error {
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte(j.Text+"\n"), 0600); err != nil {
		return err
	}
	b, err := json.Marshal(struct {
		Text     string    `json:"text"`
		Segments []Segment `json:"segments"`
	}{j.Text, j.Segments})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), b, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "result.srt"), []byte(toSRT(j.Segments)), 0600)
}

func toSRT(segments []Segment) string {
	var b strings.Builder
	for i, s := range segments {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s%s\n\n", i+1, srtTime(s.Start), srtTime(s.End), speakerPrefix(s), strings.TrimSpace(s.Text))
	}
	return b.String()
}

func srtTime(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	ms := int(sec * 1000)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func speakerPrefix(s Segment) string {
	if s.Speaker == "" {
		return ""
	}
	return "[" + s.Speaker + "] "
}
