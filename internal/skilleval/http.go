package skilleval

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

// HTTPRunner is the live leg: it posts the prepared space dir and prompt
// to an operator-supplied eval endpoint which must run the prompt against
// a real model and return the run receipt (R12.3).
type HTTPRunner struct {
	URL string
}

// Run implements Runner.
func (h HTTPRunner) Run(ctx context.Context, spaceDir, prompt string) (Receipt, error) {
	body, err := json.Marshal(map[string]string{"space_dir": spaceDir, "prompt": prompt})
	if err != nil {
		return Receipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Receipt{}, err
	}
	defer resp.Body.Close()
	var rc Receipt
	if err := json.NewDecoder(resp.Body).Decode(&rc); err != nil {
		return Receipt{}, err
	}
	return rc, nil
}
