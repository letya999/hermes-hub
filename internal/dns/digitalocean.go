package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// doClient speaks the DigitalOcean v2 domain-records API.
type doClient struct {
	cfg   *Config
	token string
	http  *http.Client
	base  string
}

func newDigitalOcean(cfg *Config, creds map[string]string, opts Options) (*doClient, error) {
	return &doClient{cfg: cfg, token: creds["DIGITALOCEAN_TOKEN"], http: opts.client(), base: opts.BaseURL}, nil
}

type doRecord struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	Data string `json:"data"`
	TTL  int    `json:"ttl"`
}

func (c *doClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("digitalocean http %d: %w", resp.StatusCode, ErrAuth)
		}
		return nil, fmt.Errorf("digitalocean http %d", resp.StatusCode)
	}
	return raw, nil
}

func (c *doClient) recordsPath() string {
	return "/v2/domains/" + url.PathEscape(c.cfg.Zone) + "/records"
}

func (c *doClient) records(ctx context.Context) ([]doRecord, error) {
	raw, err := c.request(ctx, http.MethodGet, c.recordsPath(), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		DomainRecords []doRecord `json:"domain_records"`
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.DomainRecords, nil
}

func (c *doClient) find(ctx context.Context, rec Record) (*doRecord, error) {
	sub, err := c.cfg.RecordName(rec.Name)
	if err != nil {
		return nil, err
	}
	all, err := c.records(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Type == rec.Type && all[i].Name == sub && all[i].Data == rec.Data {
			return &all[i], nil
		}
	}
	return nil, ErrNotFound
}

func (c *doClient) Ensure(ctx context.Context, rec Record) (Result, error) {
	sub, err := c.cfg.RecordName(rec.Name)
	if err != nil {
		return Result{}, err
	}
	_, err = c.request(ctx, http.MethodPost, c.recordsPath(), map[string]any{
		"type": rec.Type, "name": sub, "data": rec.Data, "ttl": rec.TTL,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Record: rec, EffectiveTTL: rec.TTL}, nil
}

func (c *doClient) Remove(ctx context.Context, rec Record) error {
	existing, err := c.find(ctx, rec)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", c.recordsPath(), existing.ID), nil)
	return err
}

func (c *doClient) List(ctx context.Context) ([]Record, error) {
	all, err := c.records(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(all))
	for _, r := range all {
		out = append(out, Record{Name: r.Name, Type: r.Type, Data: r.Data, TTL: r.TTL})
	}
	return out, nil
}
