package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Question is one live-eval probe (R7.3).
type Question struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Lang     string   `json:"lang"`
	Text     string   `json:"text"`
	Expect   []string `json:"expect"`
}

// LiveResult is one judged answer; a relay failure is recorded, not silent.
type LiveResult struct {
	ID      string   `json:"id"`
	Cited   []string `json:"cited"`
	Correct bool     `json:"correct"`
	Error   string   `json:"error,omitempty"`
}

// LiveReport is the honest record of a live run.
type LiveReport struct {
	Live      bool         `json:"live"`
	Questions int          `json:"questions"`
	Correct   int          `json:"correct"`
	Accuracy  float64      `json:"accuracy"`
	Results   []LiveResult `json:"results"`
}

// Asker is the model seam — production is the model-relay call.
type Asker func(ctx context.Context, system, user string) (string, error)

const liveSystem = "You answer questions about a personal memory vault. Answer concisely and cite the evidence refs (path:line-end) you relied on."

var evRef = regexp.MustCompile(`[\w./-]+\.md:\d+-\d+`)

// RunLive asks every question through the relay and judges each answer by
// whether it cites an expected evidence ref.
func RunLive(ctx context.Context, ask Asker, qs []Question) LiveReport {
	rep := LiveReport{Live: true, Questions: len(qs)}
	for _, q := range qs {
		r := LiveResult{ID: q.ID}
		ans, err := ask(ctx, liveSystem, q.Text)
		if err != nil {
			r.Error = err.Error()
		} else {
			r.Cited = evRef.FindAllString(ans, -1)
			for _, c := range r.Cited {
				for _, e := range q.Expect {
					if strings.Contains(c, e) {
						r.Correct = true
					}
				}
			}
		}
		if r.Correct {
			rep.Correct++
		}
		rep.Results = append(rep.Results, r)
	}
	rep.Accuracy = float64(rep.Correct) / float64(rep.Questions)
	return rep
}

// RelayAsker is the production model seam: one bounded chat-completions POST
// to the managed model-relay (HUB_MODEL_RELAY_URL + HUB_MODEL_RELAY_AUTH) —
// same contract as the night rollup's relay call.
func RelayAsker() Asker {
	return func(ctx context.Context, system, user string) (string, error) {
		base := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_URL"))
		if base == "" {
			return "", errors.New("memory eval: HUB_MODEL_RELAY_URL unset")
		}
		payload, err := json.Marshal(map[string]any{
			"model": "",
			"messages": []map[string]string{
				{"role": "system", "content": system},
				{"role": "user", "content": user},
			},
		})
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(base, "/")+"/v1/chat/completions", bytes.NewReader(payload)) // #nosec G704 -- operator-set relay URL
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if auth := strings.TrimSpace(os.Getenv("HUB_MODEL_RELAY_AUTH")); auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		client := &http.Client{Timeout: 120 * time.Second}
		resp, err := client.Do(req) // #nosec G704 -- operator-set relay URL
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil || resp.StatusCode != http.StatusOK {
			return "", errors.New("memory eval: relay request failed")
		}
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &out) != nil || len(out.Choices) == 0 {
			return "", errors.New("memory eval: bad relay response")
		}
		return out.Choices[0].Message.Content, nil
	}
}
