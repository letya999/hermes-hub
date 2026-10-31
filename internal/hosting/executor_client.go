package hosting

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type RelayRequest struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}
type RelayResponse struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   []byte      `json:"body"`
}
type ExecutorRequest struct {
	Action   string        `json:"action"`
	Workload Workload      `json:"workload"`
	Request  *RelayRequest `json:"request,omitempty"`
}
type ControllerClient struct {
	URL, Token string
	HTTP       *http.Client
}

func (c *ControllerClient) call(ctx context.Context, in ExecutorRequest) (RelayResponse, error) {
	var out RelayResponse
	if c.HTTP == nil || c.Token == "" || !PrivateEndpoint(c.URL, "8545", "/hosting") {
		return out, ErrUnavailable
	}
	body, err := json.Marshal(in)
	if err != nil {
		return out, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body)) // #nosec G704 -- immutable operator controller URL, allowlisted before this call.
	if err != nil {
		return out, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := PrivateClient(c.HTTP).Do(req) // #nosec G704 -- allowlisted internal controller; private client disables proxies, redirects and ambient cookies.
	if err != nil {
		return out, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return out, ErrUnavailable
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 12<<20))
	if d.Decode(&out) != nil || d.Decode(new(any)) != io.EOF {
		return out, ErrUnavailable
	}
	return out, nil
}
func (c *ControllerClient) Ensure(ctx context.Context, w Workload) error {
	_, err := c.call(ctx, ExecutorRequest{Action: "ensure", Workload: w})
	return err
}
func (c *ControllerClient) Remove(ctx context.Context, w Workload) error {
	_, err := c.call(ctx, ExecutorRequest{Action: "remove", Workload: w})
	return err
}
func (c *ControllerClient) RoundTrip(ctx context.Context, w Workload, r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return nil, ErrQuota
	}
	headers := http.Header{}
	for _, key := range []string{"Accept", "Content-Type", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(key); v != "" {
			headers.Set(key, v)
		}
	}
	if value := appRequestCookies(w, r); value != "" {
		headers.Set("Cookie", value)
	}
	out, err := c.call(ctx, ExecutorRequest{Action: "request", Workload: w, Request: &RelayRequest{r.Method, r.URL.RequestURI(), headers, body}})
	if err != nil {
		return nil, err
	}
	if out.Status < 200 || out.Status > 599 || len(out.Body) > 8<<20 {
		return nil, ErrUnavailable
	}
	return &http.Response{StatusCode: out.Status, Header: out.Header, Body: io.NopCloser(bytes.NewReader(out.Body)), ContentLength: int64(len(out.Body))}, nil
}

func (c *ControllerClient) Check(ctx context.Context, w Workload) error {
	_, err := c.call(ctx, ExecutorRequest{Action: "check", Workload: w})
	return err
}
