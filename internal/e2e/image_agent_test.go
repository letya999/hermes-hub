package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestImageAgentNeedsUnknownPixelsAndActualJPEG(t *testing.T) {
	for _, name := range []string{"image_convert", "image_inspect", "image_generate", "image_edit"} {
		for _, outcome := range []string{"ok", "lie", "corrupt"} {
			if outcome == "corrupt" && name != "image_edit" {
				continue
			}
			lie := outcome == "lie"
			t.Run(name+outcome, func(t *testing.T) {
				root := t.TempDir()
				for _, dir := range []string{"mappings", "mock-sent", "done"} {
					if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
						t.Fatal(err)
					}
				}
				files := map[string]string{}
				var picture image.Image
				server := mcp.NewServer(&mcp.Implementation{Name: "image-fixture", Version: "1"}, nil)
				for _, operation := range []string{name, "file_write", "code_exec", "artifact_remove"} {
					server.AddTool(&mcp.Tool{Name: operation, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						var args struct{ Path, Text, Command string }
						if err := json.Unmarshal(r.Params.Arguments, &args); err != nil {
							t.Fatal(err)
						}
						if !strings.HasPrefix(args.Path, "artifacts/") || !strings.Contains(args.Path, "e2e-") {
							t.Fatal("foreign image path")
						}
						switch operation {
						case "file_write":
							files[args.Path] = args.Text
						case "code_exec":
							if strings.Contains(args.Command, "hashlib.sha256") {
								if !strings.HasSuffix(args.Path, "/exports") || !strings.Contains(args.Command, `os.environ["INPUTS"]`) || strings.Contains(args.Command, "OUTPUTS") {
									t.Fatal("checksum escaped read-only scratch inputs")
								}
								sum := sha256.Sum256([]byte(files[args.Path+"/input.png"]))
								return &mcp.CallToolResult{StructuredContent: map[string]any{"exit_code": 0, "stdout": fmt.Sprintf("%x\n", sum)}}, nil
							}
							if !strings.Contains(args.Command, "seed.txt") {
								t.Fatal("unbounded preparation")
							}
							data, err := base64.StdEncoding.DecodeString(files[args.Path+"/seed.txt"])
							if err != nil {
								t.Fatal(err)
							}
							picture, err = png.Decode(bytes.NewReader(data))
							if err != nil {
								t.Fatal(err)
							}
							files[args.Path+"/exports/input.png"] = string(data)
							return &mcp.CallToolResult{StructuredContent: map[string]any{"exit_code": 0}}, nil
						case "artifact_remove":
							if _, ok := files[args.Path]; !ok {
								return &mcp.CallToolResult{IsError: true}, nil
							}
							delete(files, args.Path)
						default:
							t.Fatal("oracle executed image operation instead of agent")
						}
						return &mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}, nil
					})
				}
				hs := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
					if r.Header.Get("Authorization") != "Bearer synthetic-owner" {
						t.Fatal("wrong owner")
					}
					return server
				}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
				defer hs.Close()
				token := filepath.Join(root, "tokens.json")
				data, _ := json.Marshal(map[string]identity.Envelope{"synthetic-owner": {PrincipalID: "alice"}})
				if err := os.WriteFile(token, data, 0600); err != nil {
					t.Fatal(err)
				}
				client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host != "127.0.0.1:8081" {
						return http.DefaultTransport.RoundTrip(r)
					}
					var input struct {
						Text   string
						ChatID int64 `json:"chat_id"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					marker := regexp.MustCompile(`E2E [a-f0-9]{16}`).FindString(input.Text)
					text := marker
					id := "mock-" + strings.Repeat("a", 64)
					delivery := communication.Delivery{ID: "job-" + id + "-response", JobID: id, TaskID: "task", ChatID: input.ChatID, Channel: "mock"}
					if strings.Contains(input.Text, "Use image_") {
						if name == "image_inspect" {
							red, green, blue, _ := picture.At(0, 0).RGBA()
							color := "blue"
							if red > 0 {
								color = "red"
							}
							if green > 0 {
								color = "green"
							}
							if red > 0 && green > 0 {
								color = "yellow"
							}
							if blue > 0 {
								color = "blue"
							}
							text += " " + color
							if lie {
								text = marker + " invented"
							}
						}
						if name == "image_convert" && !lie {
							nonce := regexp.MustCompile(`name (e2e-[a-f0-9]{16})`).FindStringSubmatch(input.Text)[1]
							target := "artifacts/images/" + nonce + ".jpg"
							files[target] = "own JPEG"
							var jpegData bytes.Buffer
							if err := jpeg.Encode(&jpegData, picture, nil); err != nil {
								t.Fatal(err)
							}
							delivery.Artifacts = []runtime.ArtifactRef{{Name: "own.jpg", Path: "images/" + nonce + ".jpg", Blob: "own.jpg", Size: int64(jpegData.Len())}}
						}
						if (name == "image_generate" || name == "image_edit") && !lie {
							nonce := regexp.MustCompile(`name (e2e-[a-f0-9]{16})`).FindStringSubmatch(input.Text)[1]
							files["artifacts/images/"+nonce+".png"] = "generated PNG"
							delivery.Artifacts = []runtime.ArtifactRef{{Name: "own.png", Path: "images/" + nonce + ".png", Blob: "own.png"}}
							if outcome == "corrupt" {
								for path := range files {
									if strings.HasSuffix(path, "/input.png") {
										files[path] = "corrupted source"
									}
								}
							}
						}
					}
					delivery.Text = text
					mapping := communication.JobMapping{JobID: id, PrincipalID: "alice", TaskID: "task", SessionID: "session", RunID: "run", Status: "completed"}
					for file, value := range map[string]any{"done/" + id + ".json": communication.Job{ID: id, Envelope: identity.Envelope{PrincipalID: "alice"}}, "mappings/" + id + ".json": mapping, "mock-sent/" + delivery.ID + ".json": map[string]any{"principal": "alice", "delivery": delivery, "artifact_data": func() map[string][]byte {
						if len(delivery.Artifacts) == 0 {
							return nil
						}
						var b bytes.Buffer
						if strings.HasSuffix(delivery.Artifacts[0].Path, ".png") {
							_ = png.Encode(&b, picture)
						} else {
							_ = jpeg.Encode(&b, picture, nil)
						}
						return map[string][]byte{delivery.Artifacts[0].Name: b.Bytes()}
					}()}} {
						data, _ := json.Marshal(value)
						if err := os.WriteFile(filepath.Join(root, file), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"job_id":%q,"task_id":"task"}`, id)))}, nil
				})}
				cfg := communication.Config{SpoolDir: root, ToolHubURL: hs.URL, ControlTokensFile: token, ControlAuth: "synthetic-admin"}
				status, reason := imageAgentCase(context.Background(), client, cfg, communication.User{ID: "alice"}, "mock-server", name)
				want := "PASS"
				if lie || outcome == "corrupt" {
					want = "FAIL"
				}
				if status != want {
					t.Fatal(status, reason)
				}
				if !lie && len(files) != 0 {
					t.Fatal("own image fixture retained")
				}
			})
		}
	}
}

func TestIndependentImageFetchUsesCompletedOwnerEnvelope(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "done"), 0700); err != nil {
		t.Fatal(err)
	}
	jobID := "routine-" + strings.Repeat("a", 64)
	job := communication.Job{ID: jobID, Envelope: identity.Envelope{PrincipalID: "alice"}}
	data, _ := json.Marshal(job)
	if err := os.WriteFile(filepath.Join(root, "done", jobID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request runtime.ArtifactRequest
		if r.URL.Path != "/v1/artifact" || r.Header.Get("Authorization") != "Bearer synthetic-supervisor" || json.NewDecoder(r.Body).Decode(&request) != nil || request.PrincipalID != "alice" || request.JobID != jobID || request.Name != "images/own.jpg" || request.Consume {
			t.Error("wrong artifact authority or consuming fetch")
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte("own JPEG bytes"))
	}))
	defer server.Close()
	cfg := communication.Config{SpoolDir: root, RuntimeURL: server.URL, RuntimeAuth: "synthetic-supervisor"}
	actual, err := fetchOwnImage(context.Background(), server.Client(), cfg, "alice", jobID, "images/own.jpg")
	if err != nil || string(actual) != "own JPEG bytes" {
		t.Fatal(string(actual), err)
	}
	for _, owner := range []string{"bob", ""} {
		if _, err := fetchOwnImage(context.Background(), server.Client(), cfg, owner, jobID, "images/own.jpg"); err == nil {
			t.Fatal("another owner fetched image")
		}
	}
	if _, err := fetchOwnImage(context.Background(), server.Client(), cfg, "alice", "../escape", "images/own.jpg"); err == nil {
		t.Fatal("job traversal accepted")
	}
	if calls != 1 {
		t.Fatal("invalid owner reached runtime", calls)
	}
}
