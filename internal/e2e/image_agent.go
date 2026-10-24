package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/HugoSmits86/nativewebp"
	"github.com/letya999/hermes-hub/internal/communication"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func imageAgentCase(ctx context.Context, client *http.Client, config communication.Config, user communication.User, mode, name string) (status, reason string) {
	for _, tool := range []string{name, "file_write", "code_exec", "artifact_remove"} {
		if !admittedHubTool(ctx, client, config, user, tool) {
			return "BLOCKED", "owner image fixture and cleanup grants required"
		}
	}
	if status, reason := mockServerCase(ctx, client, config, user, "text"); status != "PASS" {
		return status, reason
	}
	session, err := ownerHubSession(ctx, client, config, user)
	if err != nil {
		return "BLOCKED", "owner ToolHub session unavailable"
	}
	defer session.Close()
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "FAIL", "random image fixture unavailable"
	}
	id := "e2e-" + hex.EncodeToString(nonce)
	marker := "E2E " + hex.EncodeToString(nonce)
	directory := "artifacts/documents/" + id
	width, height := 43+int(nonce[0]%20), 31+int(nonce[1]%20)
	colors := []color.NRGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, A: 255}}
	labels := []string{"red", "green", "blue", "yellow"}
	chosen := int(nonce[2] % 4)
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, colors[chosen])
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		return "FAIL", "PNG fixture encoding failed"
	}
	seed := directory + "/seed.txt"
	source := directory + "/exports/input.png"
	target := "artifacts/images/" + id + ".jpg"
	if _, err := hubToolObject(ctx, session, "file_write", map[string]any{"path": seed, "text": base64.StdEncoding.EncodeToString(data.Bytes())}); err != nil {
		return "FAIL", "image fixture seed failed"
	}
	secondSeed := directory + "/second-seed.txt"
	secondSource := directory + "/exports/second.png"
	secondColor := (chosen + 1 + int(nonce[3]%3)) % len(colors)
	if name == "images_inspect" {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				img.SetNRGBA(x, y, colors[secondColor])
			}
		}
		var second bytes.Buffer
		if err := png.Encode(&second, img); err != nil {
			return "FAIL", "second PNG fixture encoding failed"
		}
		if _, err := hubToolObject(ctx, session, "file_write", map[string]any{"path": secondSeed, "text": base64.StdEncoding.EncodeToString(second.Bytes())}); err != nil {
			return "FAIL", "second image seed failed; first seed retained"
		}
	}
	terminal := false
	defer func() {
		if !terminal {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		paths := []string{seed, source}
		if name == "images_inspect" {
			paths = append(paths, secondSeed, secondSource)
		}
		if name == "image_convert" || name == "image_generate" || name == "image_edit" {
			paths = append(paths, target)
		}
		for _, path := range paths {
			if _, err := hubToolObject(cleanup, session, "artifact_remove", map[string]any{"path": path}); err != nil {
				status, reason = "FAIL", "own image fixture cleanup failed"
			}
		}
	}()
	command := `python -c 'import base64,os,pathlib; p=pathlib.Path(os.environ["INPUTS"])/"seed.txt"; (pathlib.Path(os.environ["OUTPUTS"])/"input.png").write_bytes(base64.b64decode(p.read_text()))'`
	if name == "images_inspect" {
		command = `python -c 'import base64,os,pathlib; pairs=[("seed.txt","input.png"),("second-seed.txt","second.png")]; [(pathlib.Path(os.environ["OUTPUTS"])/dst).write_bytes(base64.b64decode((pathlib.Path(os.environ["INPUTS"])/src).read_text())) for src,dst in pairs]'`
	}
	prepared, err := hubToolObject(ctx, session, "code_exec", map[string]any{"path": directory, "command": command})
	if err != nil || prepared["exit_code"] != float64(0) {
		return "FAIL", "actual isolated PNG preparation failed; seed retained"
	}
	prompt := fmt.Sprintf("Use image_convert only on %s with name %s, format jpeg. Return exactly %s after success. Do not touch other files.", source, id, marker)
	expected := marker
	if name == "images_inspect" {
		prompt = fmt.Sprintf("Use images_inspect exactly once on these two paths in order: %s and %s. Return exactly %s followed by one space and the dominant color of each image in the same order, separated by a space, in English (red, green, blue or yellow). Do not change files or call other image tools.", source, secondSource, marker)
		expected += " " + labels[chosen] + " " + labels[secondColor]
	}
	if name == "image_inspect" {
		prompt = fmt.Sprintf("Use image_inspect only on %s. Return exactly %s followed by one space and its dominant color in English, one of red, green, blue, yellow. Do not change any files.", source, marker)
		expected += " " + labels[chosen]
	}
	if name == "image_generate" {
		prompt = fmt.Sprintf("Use image_generate once with name %s and prompt 'A plain red square on a white background, no text'. Return exactly %s after success. Do not touch other files.", id, marker)
	}
	if name == "image_edit" {
		prompt = fmt.Sprintf("Use image_edit once on %s with name %s and prompt 'Add a large white circle in the center of this image'. Return exactly %s after success. Preserve the source.", source, id, marker)
	}
	run := mockPromptJobCase
	if mode == "telegram-server" {
		run = serverPromptJobCase
	}
	job := ""
	status, reason = run(ctx, client, config, user, "text", prompt, expected, &job)
	if status != "PASS" {
		return status, reason + "; own image retained"
	}
	terminal = true
	if name == "images_inspect" {
		denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"paths": []string{source, secondSource, directory + "/exports/unavailable.png"}}})
		if err != nil || denied == nil || !denied.IsError {
			return "FAIL", "unavailable third image did not reject the entire comparison"
		}
	}
	if name == "image_convert" {
		if err := checkConvertedImage(config.SpoolDir, mode, user.ID, job, "images/"+id+".jpg", width, height, func(path string) ([]byte, error) { return fetchOwnImage(ctx, client, config, user.ID, job, path) }); err != nil {
			return "FAIL", err.Error()
		}
	}
	if name == "image_generate" || name == "image_edit" {
		path, err := checkDeliveredImage(config.SpoolDir, mode, user.ID, job, "images/"+id, 0, 0, func(path string) ([]byte, error) { return fetchOwnImage(ctx, client, config, user.ID, job, path) })
		if path != "" {
			target = "artifacts/" + path
		} else {
			terminal = false
		}
		if err != nil {
			return "FAIL", err.Error() + "; own generation fixture retained if output is unknown"
		}
		if name == "image_edit" {
			// Text reads deliberately reject PNGs; hash through read-only scratch inputs.
			command := `python -c 'import hashlib,os,pathlib; print(hashlib.sha256((pathlib.Path(os.environ["INPUTS"])/"input.png").read_bytes()).hexdigest())'`
			original, err := hubToolObject(ctx, session, "code_exec", map[string]any{"path": directory + "/exports", "command": command})
			sum := sha256.Sum256(data.Bytes())
			stdout, _ := original["stdout"].(string)
			if err != nil || original["exit_code"] != float64(0) || strings.TrimSpace(stdout) != hex.EncodeToString(sum[:]) {
				return "FAIL", "image edit changed or lost the original"
			}
		}
	}
	return "PASS", reason + "; synthetic image result independently verified; own image files removed"
}

func checkConvertedImage(root, mode, principal, job, path string, width, height int, fetch func(string) ([]byte, error)) error {
	_, err := checkDeliveredImage(root, mode, principal, job, path, width, height, fetch)
	return err
}

func checkDeliveredImage(root, mode, principal, job, path string, width, height int, fetch func(string) ([]byte, error)) (string, error) {
	deliveryID := "job-" + job + "-response"
	receiptPath := filepath.Join(root, "outbox", "done", deliveryID+".json")
	if mode == "mock-server" {
		receiptPath = filepath.Join(root, "mock-sent", deliveryID+".json")
	}
	data, err := os.ReadFile(receiptPath)
	var delivery communication.Delivery
	var captured map[string][]byte
	if mode == "mock-server" {
		var wrapped struct {
			Principal    string                 `json:"principal"`
			Delivery     communication.Delivery `json:"delivery"`
			ArtifactData map[string][]byte      `json:"artifact_data"`
		}
		if json.Unmarshal(data, &wrapped) != nil || wrapped.Principal != principal {
			return "", fmt.Errorf("image receipt owner mismatch")
		}
		delivery, captured = wrapped.Delivery, wrapped.ArtifactData
	} else if json.Unmarshal(data, &delivery) != nil {
		return "", fmt.Errorf("image receipt invalid")
	}
	if err != nil || delivery.ID != deliveryID || delivery.JobID != job {
		return "", fmt.Errorf("image receipt job mismatch")
	}
	for _, artifact := range delivery.Artifacts {
		generated := width == 0 && height == 0
		if (!generated && artifact.Path != path) || (generated && (strings.TrimSuffix(artifact.Path, filepath.Ext(artifact.Path)) != path || !regexp.MustCompile(`\.(png|jpg|webp)$`).MatchString(artifact.Path))) {
			continue
		}
		if artifact.Error != "" || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(artifact.Blob) || artifact.Blob == "." || artifact.Blob == ".." {
			return artifact.Path, fmt.Errorf("image artifact missing or invalid")
		}
		blob := captured[artifact.Name]
		if mode != "mock-server" {
			if fetch == nil {
				return artifact.Path, fmt.Errorf("independent image fetch unavailable")
			}
			blob, err = fetch(artifact.Path)
		}
		if err != nil || len(blob) > 16<<20 {
			return artifact.Path, fmt.Errorf("image blob unavailable")
		}
		actual, err := jpeg.DecodeConfig(bytes.NewReader(blob))
		if generated {
			if strings.HasSuffix(artifact.Path, ".webp") {
				actual, err = nativewebp.DecodeConfig(bytes.NewReader(blob))
			} else {
				actual, _, err = image.DecodeConfig(bytes.NewReader(blob))
			}
		}
		if err != nil || actual.Width <= 0 || actual.Height <= 0 || actual.Width > 8192 || actual.Height > 8192 || (!generated && (actual.Width != width || actual.Height != height)) {
			return artifact.Path, fmt.Errorf("actual image dimensions or format contradict delivery")
		}
		return artifact.Path, nil
	}
	return "", fmt.Errorf("image absent from actual delivery")
}

// Completed deliveries drop staged blobs; Telegram verification re-reads the
// original artifact with its exact completed job envelope, without consuming it.
func fetchOwnImage(ctx context.Context, client *http.Client, config communication.Config, principal, jobID, name string) ([]byte, error) {
	if !regexp.MustCompile(`^(mock|routine)-[a-f0-9]{64}$`).MatchString(jobID) {
		return nil, fmt.Errorf("invalid image job identity")
	}
	data, err := os.ReadFile(filepath.Join(config.SpoolDir, "done", jobID+".json"))
	var job communication.Job
	if err != nil || json.Unmarshal(data, &job) != nil || job.ID != jobID || job.Envelope.PrincipalID != principal {
		return nil, fmt.Errorf("completed image job owner mismatch")
	}
	body, err := json.Marshal(hubruntime.ArtifactRequest{ExecuteRequest: hubruntime.ExecuteRequest{Envelope: job.Envelope, JobID: job.ID, OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: job.IdempotencyKey}, Name: name})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.RuntimeURL, "/")+"/v1/artifact", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.RuntimeAuth)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("independent image fetch HTTP %d", response.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil || len(data) == 0 || len(data) > 16<<20 {
		return nil, fmt.Errorf("independent image size outside bounds")
	}
	return data, nil
}
