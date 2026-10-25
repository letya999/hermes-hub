package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// regruClient speaks REG.API v2: form POST to /api/regru2/zone/<fn> with
// username/password and a JSON input_data payload.
type regruClient struct {
	cfg  *Config
	user string
	pass string
	http *http.Client
	base string
}

func newRegru(cfg *Config, creds map[string]string, opts Options) (*regruClient, error) {
	return &regruClient{cfg: cfg, user: creds["REGRU_USERNAME"], pass: creds["REGRU_PASSWORD"],
		http: opts.client(), base: opts.BaseURL}, nil
}

type regruResponse struct {
	Result    string          `json:"result"`
	ErrorCode string          `json:"error_code"`
	Answer    json.RawMessage `json:"answer"`
}

func (c *regruClient) call(ctx context.Context, fn string, input map[string]any) (*regruResponse, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"username":      {c.user},
		"password":      {c.pass},
		"input_data":    {string(raw)},
		"input_format":  {"json"},
		"output_format": {"json"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"zone/"+fn, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out regruResponse
	if err = json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Result == "success" {
		return &out, nil
	}
	switch {
	case strings.Contains(out.ErrorCode, "NOT_FOUND"):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, out.ErrorCode)
	case strings.Contains(out.ErrorCode, "AUTH"):
		return nil, fmt.Errorf("%w: %s", ErrAuth, out.ErrorCode)
	}
	return nil, errors.New("reg.ru api: " + out.ErrorCode)
}

func (c *regruClient) Ensure(ctx context.Context, rec Record) (Result, error) {
	sub, err := c.cfg.RecordName(rec.Name)
	if err != nil {
		return Result{}, err
	}
	input := map[string]any{
		"domains":   []map[string]string{{"dname": c.cfg.Zone}},
		"subdomain": sub,
	}
	fn := "add_alias"
	switch rec.Type {
	case "TXT":
		fn = "add_txt"
		input["text"] = rec.Data
	case "AAAA":
		fn = "add_aaaa"
	}
	if rec.Type != "TXT" {
		input["ipaddr"] = rec.Data
	}
	_, err = c.call(ctx, fn, input)
	if err != nil {
		return Result{}, err
	}
	// REG.API has no per-record TTL — report the zone SOA TTL (SPEC-0076 R1).
	res := Result{Record: rec}
	if recs, lerr := c.listRR(ctx); lerr == nil {
		for _, rr := range recs {
			if rr.Rectype == "SOA" {
				res.EffectiveTTL = rr.TTL
				break
			}
		}
	}
	return res, nil
}

func (c *regruClient) Remove(ctx context.Context, rec Record) error {
	sub, err := c.cfg.RecordName(rec.Name)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, "remove_record", map[string]any{
		"domains":     []map[string]string{{"dname": c.cfg.Zone}},
		"subdomain":   sub,
		"record_type": rec.Type,
		"content":     rec.Data,
	})
	return err
}

type regruRRSet struct {
	Subname string `json:"subname"`
	Rectype string `json:"rectype"`
	Content string `json:"content"`
	TTL     int    `json:"ttl,omitempty"`
}

func (c *regruClient) listRR(ctx context.Context) ([]regruRRSet, error) {
	out, err := c.call(ctx, "get_resource_records", map[string]any{
		"domains": []map[string]string{{"dname": c.cfg.Zone}},
	})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Domains []struct {
			RRs []regruRRSet `json:"rrs"`
		} `json:"domains"`
	}
	if err = json.Unmarshal(out.Answer, &answer); err != nil {
		return nil, err
	}
	var recs []regruRRSet
	for _, d := range answer.Domains {
		recs = append(recs, d.RRs...)
	}
	return recs, nil
}

func (c *regruClient) List(ctx context.Context) ([]Record, error) {
	rrs, err := c.listRR(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]Record, 0, len(rrs))
	for _, rr := range rrs {
		recs = append(recs, Record{Name: rr.Subname, Type: rr.Rectype, Data: rr.Content})
	}
	return recs, nil
}
