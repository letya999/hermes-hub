package communication

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The forum-topic API surface (merged from feat/m12) posts the documented Bot
// API methods with chat_id/message_thread_id and parses create's topic id.
func TestForumTopicAPIMethods(t *testing.T) {
	payloads := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		method := strings.TrimPrefix(r.URL.Path, "/bottoken/")
		payload := map[string]any{}
		_ = json.Unmarshal(body, &payload)
		payloads[method] = payload
		if method == "createForumTopic" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_thread_id":77}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", time.Second)
	ctx := context.Background()

	id, err := api.CreateForumTopic(ctx, -100, "ops")
	if err != nil || id != 77 {
		t.Fatalf("CreateForumTopic: id=%d err=%v", id, err)
	}
	if payloads["createForumTopic"]["chat_id"] != float64(-100) || payloads["createForumTopic"]["name"] != "ops" {
		t.Fatalf("createForumTopic payload: %v", payloads["createForumTopic"])
	}
	for method, call := range map[string]func() error{
		"editForumTopic":   func() error { return api.EditForumTopic(ctx, -100, 77, "renamed") },
		"deleteForumTopic": func() error { return api.DeleteForumTopic(ctx, -100, 77) },
		"closeForumTopic":  func() error { return api.CloseForumTopic(ctx, -100, 77) },
		"reopenForumTopic": func() error { return api.ReopenForumTopic(ctx, -100, 77) },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if payloads[method]["chat_id"] != float64(-100) || payloads[method]["message_thread_id"] != float64(77) {
			t.Fatalf("%s payload: %v", method, payloads[method])
		}
	}
	if payloads["editForumTopic"]["name"] != "renamed" {
		t.Fatalf("editForumTopic name lost: %v", payloads["editForumTopic"])
	}
}

// EditMessageText/SendMessageReceipt ride the same call path; the receipt
// parses message_id and non-2xx replies surface as telegramStatusError.
func TestEditAndReceiptCalls(t *testing.T) {
	var editPayload, sendPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/bottoken/editMessageText":
			_ = json.Unmarshal(body, &editPayload)
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case "/bottoken/sendMessage":
			_ = json.Unmarshal(body, &sendPayload)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":512}}`))
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", time.Second)
	ctx := context.Background()

	if err := api.EditMessageText(ctx, 11, 5, "done"); err != nil {
		t.Fatal(err)
	}
	if editPayload["message_id"] != float64(5) || editPayload["text"] != "done" {
		t.Fatalf("editMessageText payload: %v", editPayload)
	}
	receiptAPI, ok := api.(interface {
		SendMessageReceipt(context.Context, int64, int64, string, string) (int, error)
	})
	if !ok {
		t.Fatal("telegramAPI lacks SendMessageReceipt")
	}
	mid, err := receiptAPI.SendMessageReceipt(ctx, -100, 7, "text", "")
	if err != nil || mid != 512 {
		t.Fatalf("SendMessageReceipt: mid=%d err=%v", mid, err)
	}
	if sendPayload["message_thread_id"] != float64(7) {
		t.Fatalf("group receipt must use message_thread_id: %v", sendPayload)
	}
	if err := api.EditMessageText(ctx, 11, 5, ""); err == nil {
		t.Fatal("empty edit accepted")
	}
	if _, err := receiptAPI.SendMessageReceipt(ctx, 11, 0, "", ""); err == nil {
		t.Fatal("empty receipt accepted")
	}
}

// The media send methods post multipart forms with the right field names and
// enforce their size/name guards before touching the wire.
func TestMediaSendMethods(t *testing.T) {
	forms := map[string]map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bottoken/")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		fields := map[string]string{}
		for key := range r.MultipartForm.Value {
			fields[key] = r.MultipartForm.Value[key][0]
		}
		for name := range r.MultipartForm.File {
			fields["file:"+name] = "1"
		}
		forms[method] = fields
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", time.Second).(*telegramAPI)
	ctx := context.Background()

	if err := api.SendDocument(ctx, -100, 7, "r.pdf", "cap", []byte("pdf-bytes")); err != nil {
		t.Fatal(err)
	}
	if err := api.SendVideo(ctx, -100, 7, "v.mp4", "", []byte("video")); err != nil {
		t.Fatal(err)
	}
	if err := api.SendPhoto(ctx, -100, 0, "p.png", "cap", []byte("png")); err != nil {
		t.Fatal(err)
	}
	if err := api.SendVoice(ctx, -100, 7, []byte("ogg"), "cap"); err != nil {
		t.Fatal(err)
	}
	if forms["sendDocument"]["file:document"] != "1" || forms["sendDocument"]["message_thread_id"] != "7" {
		t.Fatalf("sendDocument form: %v", forms["sendDocument"])
	}
	if forms["sendVideo"]["supports_streaming"] != "true" || forms["sendVideo"]["file:video"] != "1" {
		t.Fatalf("sendVideo form: %v", forms["sendVideo"])
	}
	if forms["sendVoice"]["file:voice"] != "1" {
		t.Fatalf("sendVoice form: %v", forms["sendVoice"])
	}
	// Guards: empty payloads and oversized data are rejected client-side.
	if err := api.SendDocument(ctx, -100, 0, "", "", nil); err == nil {
		t.Fatal("empty document accepted")
	}
	if err := api.SendVideo(ctx, -100, 0, "", "", nil); err == nil {
		t.Fatal("empty video accepted")
	}
	if err := api.SendPhoto(ctx, -100, 0, "", "", nil); err == nil {
		t.Fatal("empty photo accepted")
	}
	if err := api.SendVoice(ctx, -100, 0, nil, ""); err == nil {
		t.Fatal("empty voice accepted")
	}
}

func TestTelegramStatusErrorText(t *testing.T) {
	if got := (telegramStatusError{code: 409}).Error(); !strings.Contains(got, "409") {
		t.Fatalf("status error lost the code: %q", got)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", time.Second)
	err := api.DeleteMessage(context.Background(), 11, 1)
	if se, ok := err.(telegramStatusError); !ok || se.code != http.StatusConflict {
		t.Fatalf("non-2xx must surface as telegramStatusError: %v", err)
	}
}
