package communication

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	mediaSizeLimit     = 8 << 20
	mediaDurationLimit = 120
	sttTimeout         = 60 * time.Second
)

// MediaEnvelope is the bounded voice/file record. Canonical job text stays text.
type MediaEnvelope struct {
	FileID           string `json:"file_id"`
	MIME             string `json:"mime"`
	Size             int64  `json:"size"`
	Duration         int    `json:"duration"`
	TempPath         string `json:"temp_path,omitempty"`
	TranscriptStatus string `json:"transcript_status"`
	ConversationID   string `json:"conversation_id"`
	Provider         string `json:"provider"`
}

type Transcriber interface {
	Transcribe(ctx context.Context, path, mime string) (string, error)
}

type Synthesizer interface {
	Synthesize(ctx context.Context, text string) ([]byte, string, error)
}

func commandTranscriber(command string) Transcriber {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return CommandTranscriber{Command: command, Timeout: sttTimeout}
}

func commandSynthesizer(command string) Synthesizer {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return CommandSynthesizer{Command: command, Timeout: 30 * time.Second}
}

type CommandTranscriber struct {
	Command string
	Timeout time.Duration
}

func (c CommandTranscriber) Transcribe(ctx context.Context, path, mime string) (string, error) {
	if c.Command == "" {
		return "", errors.New("stt is not configured")
	}
	if c.Timeout <= 0 {
		c.Timeout = sttTimeout
	}
	jobCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, c.Command, path, mime)
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			return "", errors.New("stt timed out")
		}
		return "", errors.New("stt failed")
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", errors.New("empty transcript")
	}
	return text, nil
}

type CommandSynthesizer struct {
	Command string
	Timeout time.Duration
}

func (c CommandSynthesizer) Synthesize(ctx context.Context, text string) ([]byte, string, error) {
	if c.Command == "" || strings.TrimSpace(text) == "" {
		return nil, "", errors.New("tts is not configured")
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	jobCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, c.Command)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return nil, "", errors.New("tts failed")
	}
	if len(out) == 0 || len(out) > mediaSizeLimit {
		return nil, "", errors.New("tts output rejected")
	}
	return out, "audio/ogg", nil
}

func (g *Gateway) handleVoice(ctx context.Context, update Update, user User, message *Message) error {
	media := message.Voice
	if media == nil {
		media = message.Audio
	}
	envelope := MediaEnvelope{FileID: media.FileID, MIME: media.MimeType, Size: media.FileSize, Duration: media.Duration, ConversationID: user.envelope(message.From.ID).ConversationID, Provider: "telegram_bot"}
	if err := rejectMedia(envelope); err != nil {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-media", message.Chat.ID, "Голосовое сообщение отклонено: слишком большое, длинное или неподдерживаемый тип.")
	}
	path, err := g.downloadTelegramMedia(ctx, media.FileID)
	if path != "" {
		defer os.Remove(path)
	}
	if err != nil {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-media", message.Chat.ID, "Не удалось загрузить голосовое сообщение.")
	}
	envelope.TempPath = path
	transcript, sttErr := g.transcribe(ctx, path, envelope.MIME)
	if sttErr != nil || strings.TrimSpace(transcript) == "" {
		envelope.TranscriptStatus = "error"
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-stt", message.Chat.ID, "Не удалось распознать голосовое сообщение. Отправьте текст.")
	}
	envelope.TranscriptStatus = "ok"
	caption := strings.TrimSpace(message.Caption)
	text := "[voice mime=" + envelope.MIME + " duration=" + strconv.Itoa(envelope.Duration) + " transcript=ok]\n" + transcript
	if caption != "" {
		text = caption + "\n" + text
	}
	return g.enqueueChannelJob(ctx, user, "telegram_bot", "telegram-"+strconv.Itoa(update.UpdateID), "telegram:"+strconv.Itoa(update.UpdateID), message.Chat.ID, message.MessageID, text, user.envelope(message.From.ID))
}

func (g *Gateway) handleFile(ctx context.Context, update Update, user User, message *Message) error {
	media := message.Document
	if media == nil && len(message.Photo) > 0 {
		media = &message.Photo[len(message.Photo)-1]
		if media.MimeType == "" {
			media.MimeType = "image/jpeg"
		}
	}
	if media == nil {
		return nil
	}
	envelope := MediaEnvelope{FileID: media.FileID, MIME: media.MimeType, Size: media.FileSize, Duration: media.Duration, ConversationID: user.envelope(message.From.ID).ConversationID, Provider: "telegram_bot"}
	if envelope.Size > mediaSizeLimit {
		return g.queueDelivery(ctx, "telegram-"+strconv.Itoa(update.UpdateID)+"-file", message.Chat.ID, "Файл слишком большой.")
	}
	name := media.FileName
	if name == "" {
		name = media.FileID
	}
	text := strings.TrimSpace(message.Caption)
	meta := "[file name=" + name + " mime=" + envelope.MIME + " size=" + strconv.FormatInt(envelope.Size, 10) + "]"
	if text == "" {
		text = meta
	} else {
		text = text + "\n" + meta
	}
	return g.enqueueChannelJob(ctx, user, "telegram_bot", "telegram-"+strconv.Itoa(update.UpdateID), "telegram:"+strconv.Itoa(update.UpdateID), message.Chat.ID, message.MessageID, text, user.envelope(message.From.ID))
}

func rejectMedia(e MediaEnvelope) error {
	if e.FileID == "" {
		return errors.New("missing file")
	}
	if e.Size > mediaSizeLimit {
		return errors.New("too large")
	}
	if e.Duration > mediaDurationLimit {
		return errors.New("too long")
	}
	if e.MIME != "" && !allowedAudioMIME(e.MIME) {
		return errors.New("unsupported type")
	}
	return nil
}

func allowedAudioMIME(mime string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "audio/ogg", "audio/mpeg", "audio/mp3", "audio/wav", "audio/x-wav", "audio/webm", "audio/mp4", "audio/x-m4a", "application/octet-stream":
		return true
	default:
		return false
	}
}

func (g *Gateway) transcribe(ctx context.Context, path, mime string) (string, error) {
	if g.transcriber == nil {
		return "", errors.New("stt is not configured")
	}
	return g.transcriber.Transcribe(ctx, path, mime)
}

func (g *Gateway) downloadTelegramMedia(ctx context.Context, fileID string) (string, error) {
	if g.api == nil || g.spool == nil {
		return "", errors.New("media download unavailable")
	}
	file, err := g.api.GetFile(ctx, fileID)
	if err != nil || file.FilePath == "" {
		return "", errors.New("telegram file lookup failed")
	}
	if file.FileSize > mediaSizeLimit {
		return "", errors.New("too large")
	}
	tmp, err := os.CreateTemp(filepath.Join(g.spool.root, "tmp"), "media-*")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if err := g.api.DownloadFile(ctx, file.FilePath, tmp); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	info, err := tmp.Stat()
	if err != nil || info.Size() > mediaSizeLimit {
		_ = os.Remove(tmp.Name())
		return "", errors.New("too large")
	}
	return tmp.Name(), nil
}
