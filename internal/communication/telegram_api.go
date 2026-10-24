package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Telegram API subset used by the adapter.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	EditedMessage *Message       `json:"edited_message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// CallbackQuery is a Telegram inline-keyboard tap. Data resolves through the
// durable callback store; Message carries the originating chat.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *TGUser  `json:"from,omitempty"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}
type Message struct {
	MessageID int     `json:"message_id"`
	From      *TGUser `json:"from,omitempty"`
	Chat      TGChat  `json:"chat"`
	Text      string  `json:"text,omitempty"`
	Caption   string  `json:"caption,omitempty"`
	// DirectMessagesTopic is set on messages posted inside a private-chat
	// topic (Bot API direct_messages_topic). It is distinct from
	// message_thread_id — the two are not interchangeable.
	DirectMessagesTopic *TGDirectTopic `json:"direct_messages_topic,omitempty"`
	// MessageThreadID/IsTopicMessage carry group forum topics: a message
	// posted inside a forum topic has is_topic_message=true and the topic id
	// in message_thread_id.
	MessageThreadID int64 `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool  `json:"is_topic_message,omitempty"`
	// ForumTopicCreated is the group service message emitted when a user
	// creates a forum topic; the bot's own topic actions never pass the
	// verified-user gate, so they cannot echo back.
	ForumTopicCreated *TGForumTopicCreated `json:"forum_topic_created,omitempty"`
	ForumTopicEdited  *TGForumTopicEdited  `json:"forum_topic_edited,omitempty"`
	Voice             *TGMedia             `json:"voice,omitempty"`
	Audio             *TGMedia             `json:"audio,omitempty"`
	Document          *TGMedia             `json:"document,omitempty"`
	Photo             []TGMedia            `json:"photo,omitempty"`
}
type TGUser struct {
	ID int64 `json:"id"`
}
type TGDirectTopic struct {
	TopicID int64 `json:"topic_id"`
}
type TGForumTopicCreated struct {
	Name string `json:"name"`
}
type TGForumTopicEdited struct {
	Name string `json:"name,omitempty"`
}

// messageTopicID returns the topic of an inbound message: a private-chat
// direct_messages_topic id or, in groups, a forum message_thread_id. Root
// chats return 0.
func messageTopicID(m *Message) int64 {
	if m == nil {
		return 0
	}
	// DirectMessagesTopic only exists on private-chat topic updates; a group
	// forum topic instead sets is_topic_message + message_thread_id.
	if m.DirectMessagesTopic != nil && m.DirectMessagesTopic.TopicID > 0 {
		return m.DirectMessagesTopic.TopicID
	}
	if m.IsTopicMessage && m.MessageThreadID > 0 {
		return m.MessageThreadID
	}
	return 0
}

// topicField picks the Bot API reply parameter for a topic id: private chats
// take direct_messages_topic_id, groups take message_thread_id. Group and
// supergroup chat ids are negative; private chat ids are positive.
func topicField(chatID int64) string {
	if chatID < 0 {
		return "message_thread_id"
	}
	return "direct_messages_topic_id"
}

type TGChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type TGMedia struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	Duration int    `json:"duration,omitempty"`
}
type TelegramFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

type TelegramAPI interface {
	GetUpdates(context.Context, int64, int) ([]Update, error)
	SendMessage(context.Context, int64, int64, string, string) error
	SendDocument(context.Context, int64, int64, string, string, []byte) error
	SendPhoto(context.Context, int64, int64, string, string, []byte) error
	SendVideo(context.Context, int64, int64, string, string, []byte) error
	DeleteMessage(context.Context, int64, int) error
	EditMessageText(context.Context, int64, int, string) error
	SendChatAction(context.Context, int64, int64, string) error
	GetFile(context.Context, string) (TelegramFile, error)
	DownloadFile(context.Context, string, io.Writer) error
	SendVoice(context.Context, int64, int64, []byte, string) error
	SendKeyboard(context.Context, int64, int64, string, []DeliveryButton) error
	AnswerCallbackQuery(context.Context, string, string) error
	SetMyCommands(context.Context) error
	CreateForumTopic(context.Context, int64, string) (int64, error)
	EditForumTopic(context.Context, int64, int64, string) error
	DeleteForumTopic(context.Context, int64, int64) error
	CloseForumTopic(context.Context, int64, int64) error
	ReopenForumTopic(context.Context, int64, int64) error
}

type telegramAPI struct {
	baseURL string
	token   string
	client  *http.Client
}

func newTelegramAPI(baseURL, token string, timeout time.Duration) TelegramAPI {
	if timeout <= 0 {
		timeout = 35 * time.Second
	}
	return &telegramAPI{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{Timeout: timeout}}
}

func (t *telegramAPI) call(ctx context.Context, method string, payload any, result any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/"+method, bytes.NewReader(b)) // #nosec G704 -- configured Telegram API base URL
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return t.callRequest(req, result)
}

func (t *telegramAPI) callRequest(req *http.Request, result any) error {
	response, err := t.client.Do(req) // #nosec G704 -- request built on the configured Telegram API base URL
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return telegramStatusError{code: response.StatusCode}
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return errors.New("telegram API request failed")
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

// telegramStatusError carries the HTTP status of a failed Bot API call so the
// poll loop can recognize a second-poller conflict without string matching.
type telegramStatusError struct{ code int }

func (e telegramStatusError) Error() string {
	return fmt.Sprintf("telegram API returned HTTP %d", e.code)
}

var telegramConflictBackoff = time.Minute

func (t *telegramAPI) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var result []Update
	err := t.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "edited_message", "callback_query"}}, &result)
	return result, err
}
func (t *telegramAPI) SendMessage(ctx context.Context, chatID, topicID int64, text, parseMode string) error {
	if !utf8.ValidString(text) || text == "" || len(text) > 2*1024*1024 {
		return errors.New("invalid Telegram output")
	}
	payload := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if topicID > 0 {
		payload[topicField(chatID)] = topicID
	}
	return t.call(ctx, "sendMessage", payload, nil)
}
func (t *telegramAPI) SendKeyboard(ctx context.Context, chatID, topicID int64, text string, buttons []DeliveryButton) error {
	_, err := t.SendKeyboardReceipt(ctx, chatID, topicID, text, buttons)
	return err
}

func (t *telegramAPI) SendKeyboardReceipt(ctx context.Context, chatID, topicID int64, text string, buttons []DeliveryButton) (int, error) {
	if len(buttons) == 0 {
		return 0, errors.New("invalid Telegram button")
	}
	rows := make([][]map[string]string, 0, len(buttons))
	for _, button := range buttons {
		if len(button.Data) > 64 {
			return 0, errors.New("invalid Telegram button")
		}
		rows = append(rows, []map[string]string{{"text": button.Text, "callback_data": button.Data}})
	}
	payload := map[string]any{"chat_id": chatID, "text": text, "reply_markup": map[string]any{"inline_keyboard": rows}}
	if topicID > 0 {
		payload[topicField(chatID)] = topicID
	}
	var result struct {
		MessageID int `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", payload, &result)
	return result.MessageID, err
}
func (t *telegramAPI) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}
func (t *telegramAPI) SendDocument(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	if len(data) == 0 || len(data) > videoSizeLimit || name == "" {
		return errors.New("invalid Telegram document")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if topicID > 0 {
		if err := form.WriteField(topicField(chatID), strconv.FormatInt(topicID, 10)); err != nil {
			return err
		}
	}
	if caption != "" {
		if err := form.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("document", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendDocument", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return t.callRequest(req, nil)
}
func (t *telegramAPI) SendVideo(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	if len(data) == 0 || len(data) > videoSizeLimit || name == "" {
		return errors.New("invalid Telegram video")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if topicID > 0 {
		if err := form.WriteField(topicField(chatID), strconv.FormatInt(topicID, 10)); err != nil {
			return err
		}
	}
	if err := form.WriteField("supports_streaming", "true"); err != nil {
		return err
	}
	if caption != "" {
		if err := form.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("video", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendVideo", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return t.callRequest(req, nil)
}
func (t *telegramAPI) SendPhoto(ctx context.Context, chatID, topicID int64, name, caption string, data []byte) error {
	if len(data) == 0 || len(data) > mediaSizeLimit || name == "" {
		return errors.New("invalid Telegram photo")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if topicID > 0 {
		if err := form.WriteField(topicField(chatID), strconv.FormatInt(topicID, 10)); err != nil {
			return err
		}
	}
	if caption != "" {
		if err := form.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("photo", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendPhoto", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return t.callRequest(req, nil)
}
func (t *telegramAPI) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return t.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}
func (t *telegramAPI) EditMessageText(ctx context.Context, chatID int64, messageID int, text string) error {
	if !utf8.ValidString(text) || text == "" || len(text) > 2*1024*1024 {
		return errors.New("invalid Telegram output")
	}
	return t.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil)
}
func (t *telegramAPI) SendMessageReceipt(ctx context.Context, chatID, topicID int64, text, parseMode string) (int, error) {
	if !utf8.ValidString(text) || text == "" || len(text) > 2*1024*1024 {
		return 0, errors.New("invalid Telegram output")
	}
	payload := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	if topicID > 0 {
		payload[topicField(chatID)] = topicID
	}
	var result struct {
		MessageID int `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", payload, &result)
	return result.MessageID, err
}
func (t *telegramAPI) SendChatAction(ctx context.Context, chatID int64, topicID int64, action string) error {
	params := map[string]any{"chat_id": chatID, "action": action}
	if topicID > 0 {
		params[topicField(chatID)] = topicID
	}
	return t.call(ctx, "sendChatAction", params, nil)
}

// createForumTopic needs a bot with manage_topics in a forum-enabled group;
// it fails otherwise and callers fall back to plain topic routing.
func (t *telegramAPI) CreateForumTopic(ctx context.Context, chatID int64, name string) (int64, error) {
	var result struct {
		MessageThreadID int64 `json:"message_thread_id"`
	}
	err := t.call(ctx, "createForumTopic", map[string]any{"chat_id": chatID, "name": name}, &result)
	return result.MessageThreadID, err
}
func (t *telegramAPI) EditForumTopic(ctx context.Context, chatID, topicID int64, name string) error {
	return t.call(ctx, "editForumTopic", map[string]any{"chat_id": chatID, "message_thread_id": topicID, "name": name}, nil)
}
func (t *telegramAPI) DeleteForumTopic(ctx context.Context, chatID, topicID int64) error {
	return t.call(ctx, "deleteForumTopic", map[string]any{"chat_id": chatID, "message_thread_id": topicID}, nil)
}
func (t *telegramAPI) CloseForumTopic(ctx context.Context, chatID, topicID int64) error {
	return t.call(ctx, "closeForumTopic", map[string]any{"chat_id": chatID, "message_thread_id": topicID}, nil)
}
func (t *telegramAPI) ReopenForumTopic(ctx context.Context, chatID, topicID int64) error {
	return t.call(ctx, "reopenForumTopic", map[string]any{"chat_id": chatID, "message_thread_id": topicID}, nil)
}
func (t *telegramAPI) GetFile(ctx context.Context, fileID string) (TelegramFile, error) {
	var file TelegramFile
	err := t.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file)
	return file, err
}
func (t *telegramAPI) DownloadFile(ctx context.Context, filePath string, dest io.Writer) error {
	if filePath == "" || strings.Contains(filePath, "..") {
		return errors.New("invalid telegram file path")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/file/bot"+t.token+"/"+filePath, nil)
	if err != nil {
		return err
	}
	response, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("telegram file download returned HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(dest, io.LimitReader(response.Body, mediaSizeLimit+1))
	return err
}
func (t *telegramAPI) SendVoice(ctx context.Context, chatID, topicID int64, audio []byte, caption string) error {
	if len(audio) == 0 || len(audio) > mediaSizeLimit {
		return errors.New("invalid voice payload")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := form.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if topicID > 0 {
		if err := form.WriteField(topicField(chatID), strconv.FormatInt(topicID, 10)); err != nil {
			return err
		}
	}
	if caption != "" {
		if err := form.WriteField("caption", limitTelegramText(caption)); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("voice", "reply.ogg")
	if err != nil {
		return err
	}
	if _, err := part.Write(audio); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/sendVoice", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return t.callRequest(req, nil)
}

// SetMyCommands registers the canonical menu on every Telegram scope. Without
// this a stale scope registration (e.g. left by an upstream adapter) shadows
// the default menu in private and group chats.
func (t *telegramAPI) SetMyCommands(ctx context.Context) error {
	commands := make([]map[string]string, 0, len(botCommandInventory))
	for _, c := range botCommandInventory {
		commands = append(commands, map[string]string{"command": c.Command, "description": c.Description})
	}
	for _, scope := range []string{"default", "all_private_chats", "all_group_chats", "all_chat_administrators"} {
		if err := t.call(ctx, "setMyCommands", map[string]any{"commands": commands, "scope": map[string]any{"type": scope}}, nil); err != nil {
			return err
		}
	}
	return nil
}
