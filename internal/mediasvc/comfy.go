package mediasvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// comfyGen executes an operator-exported API workflow from the media volume.
type comfyGen struct {
	base, directory string
	client          *http.Client
	timeout         time.Duration
	keyFor          keyResolver
}

func (e *comfyGen) Ready(ctx context.Context) bool {
	key, err := e.keyFor(ctx)
	if err != nil {
		return false
	}
	return probe(ctx, e.client, e.base+"/system_stats", key)
}

func (e *comfyGen) Generate(ctx context.Context, req GenRequest) (GenResult, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	handle, err := e.Submit(ctx, req)
	if err != nil {
		return GenResult{}, err
	}
	for {
		result, done, err := e.Poll(ctx, handle, req.Model)
		if err != nil || done {
			return result, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return GenResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (e *comfyGen) Submit(ctx context.Context, req GenRequest) (SubmitHandle, error) {
	if len(req.Source) > 0 {
		return SubmitHandle{}, errors.New("ComfyUI workflow adapter does not support edits")
	}
	root, err := os.OpenRoot(e.directory)
	if err != nil {
		return SubmitHandle{}, errors.New("ComfyUI workflow volume unavailable")
	}
	defer root.Close()
	file, err := root.Open(req.Model + ".json")
	if err != nil {
		return SubmitHandle{}, errors.New("ComfyUI workflow unavailable")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return SubmitHandle{}, errors.New("ComfyUI workflow exceeds its bound")
	}
	var graph map[string]map[string]json.RawMessage
	if json.Unmarshal(body, &graph) != nil {
		return SubmitHandle{}, errors.New("invalid ComfyUI API workflow")
	}
	prompt, _ := json.Marshal(req.Prompt)
	found := false
	for _, node := range graph {
		if len(node["inputs"]) == 0 {
			continue
		}
		var inputs map[string]json.RawMessage
		if json.Unmarshal(node["inputs"], &inputs) != nil {
			return SubmitHandle{}, errors.New("invalid ComfyUI workflow inputs")
		}
		for name, value := range inputs {
			if string(value) == `"${PROMPT}"` {
				inputs[name] = prompt
				found = true
			}
		}
		node["inputs"], err = json.Marshal(inputs)
		if err != nil {
			return SubmitHandle{}, errors.New("invalid ComfyUI workflow inputs")
		}
	}
	if !found {
		return SubmitHandle{}, errors.New("ComfyUI workflow needs a PROMPT input marker")
	}
	var response struct {
		ID string `json:"prompt_id"`
	}
	if err := e.json(ctx, http.MethodPost, "/prompt", map[string]any{"prompt": graph}, &response); err != nil {
		return SubmitHandle{}, err
	}
	if response.ID == "" {
		return SubmitHandle{}, errSubmissionUncertain
	}
	return SubmitHandle{RemoteID: response.ID, Engine: "comfy", Model: req.Model}, nil
}

func (e *comfyGen) Poll(ctx context.Context, handle SubmitHandle, _ string) (GenResult, bool, error) {
	type output struct{ Filename, Subfolder, Type string }
	var history map[string]struct {
		Outputs map[string]struct {
			Images []output `json:"images"`
		} `json:"outputs"`
		Status struct {
			Status string `json:"status_str"`
		} `json:"status"`
	}
	if err := e.json(ctx, http.MethodGet, "/history/"+url.PathEscape(handle.RemoteID), nil, &history); err != nil {
		return GenResult{}, false, err
	}
	entry, ok := history[handle.RemoteID]
	if !ok {
		return GenResult{}, false, nil
	}
	if entry.Status.Status == "error" {
		return GenResult{}, false, errors.New("ComfyUI workflow failed")
	}
	var files []output
	for _, node := range entry.Outputs {
		for _, file := range node.Images {
			if file.Type == "output" {
				files = append(files, file)
			}
		}
	}
	if len(files) != 1 {
		return GenResult{}, false, errors.New("ComfyUI workflow must save exactly one output")
	}
	query := url.Values{"filename": {files[0].Filename}, "subfolder": {files[0].Subfolder}, "type": {"output"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/view?"+query.Encode(), nil) // #nosec G704 -- configured ComfyUI upstream endpoint
	if err != nil {
		return GenResult{}, false, errors.New("ComfyUI output reference invalid")
	}
	if err := e.authorize(ctx, request); err != nil {
		return GenResult{}, false, err
	}
	response, err := e.client.Do(request) // #nosec G704 -- configured ComfyUI upstream endpoint
	if err != nil {
		return GenResult{}, false, errors.New("ComfyUI output unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return GenResult{}, false, fmt.Errorf("ComfyUI output HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxGenResultBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxGenResultBytes {
		return GenResult{}, false, errors.New("ComfyUI output exceeds its bound")
	}
	mime := sniffMime(data)
	if !mediaMimeOK(mime) {
		return GenResult{}, false, errors.New("ComfyUI output is not supported media")
	}
	return GenResult{Data: data, Mime: mime}, true, nil
}

func (e *comfyGen) json(ctx context.Context, method, path string, payload, destination any) error {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return errors.New("invalid ComfyUI request")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, e.base+path, bytes.NewReader(body)) // #nosec G704 -- configured ComfyUI upstream endpoint
	if err != nil {
		return errors.New("invalid ComfyUI endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if err := e.authorize(ctx, request); err != nil {
		return err
	}
	response, err := e.client.Do(request) // #nosec G704 -- configured ComfyUI upstream endpoint
	if err != nil {
		if method == http.MethodPost {
			return errSubmissionUncertain
		}
		return errors.New("ComfyUI request failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 && method == http.MethodPost {
		return errSubmissionUncertain
	}
	if response.StatusCode != 200 {
		return fmt.Errorf("ComfyUI HTTP %d", response.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(destination) != nil {
		if method == http.MethodPost {
			return errSubmissionUncertain
		}
		return errors.New("invalid ComfyUI response")
	}
	return nil
}

func (e *comfyGen) authorize(ctx context.Context, request *http.Request) error {
	key, err := e.keyFor(ctx)
	if err != nil {
		return errors.New("ComfyUI credential unavailable")
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	return nil
}

func newComfyGen(c Config, client *http.Client, resolve keyResolver) (GenEngine, error) {
	parsed, err := url.Parse(c.Upstream)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("ComfyUI requires a fixed service endpoint")
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	timeout := c.JobTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &comfyGen{base: strings.TrimRight(c.Upstream, "/"), directory: c.ModelDir, client: client, timeout: timeout, keyFor: resolve}, nil
}
