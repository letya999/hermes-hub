package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func checkSelectedModel(ctx context.Context, baseURL, key, model string, client *http.Client) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return fmt.Errorf("invalid model catalog URL")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("model catalog unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("model catalog returned HTTP %d", response.StatusCode)
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&catalog) != nil || catalog.Data == nil {
		return fmt.Errorf("model catalog has an invalid response")
	}
	for _, entry := range catalog.Data {
		if entry.ID == model {
			return nil
		}
	}
	return fmt.Errorf("configured model %q is absent from provider catalog", model)
}

func resolvePublishedModelURL(ctx context.Context, baseURL, composePath string, run func(context.Context, ...string) ([]byte, error)) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() != "cliproxy" {
		return baseURL, nil
	}
	port := parsed.Port()
	if port == "" {
		return "", fmt.Errorf("cliproxy model URL needs an explicit port")
	}
	output, err := run(ctx, "compose", "-f", composePath, "port", "cliproxy", port)
	if err != nil {
		return "", fmt.Errorf("local cliproxy is not running or has no published port: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	host, publishedPort, err := net.SplitHostPort(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return "", fmt.Errorf("cliproxy model catalog requires a loopback-published port")
	}
	parsed.Host = net.JoinHostPort(host, publishedPort)
	return parsed.String(), nil
}

func doctorLiveModel(ctx context.Context, baseURL, key, model, composePath string, run func(context.Context, ...string) ([]byte, error), client *http.Client) error {
	resolved, err := resolvePublishedModelURL(ctx, baseURL, composePath, run)
	if err != nil {
		return err
	}
	return checkSelectedModel(ctx, resolved, key, model, client)
}

func doctorPortLookup(dir string, run func(context.Context, []string, []string) ([]byte, error)) func(context.Context, ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return run(ctx, supervisorComposeEnv(dir), args)
	}
}
