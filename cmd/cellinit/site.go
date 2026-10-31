package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/letya999/hermes-hub/internal/hosting"
)

// siteRelay has no destination argument: it can only reach the app's own
// loopback inside a network-none namespace. Payloads use stdin, never argv.
func siteRelay(input io.Reader, output io.Writer) error {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxResponseHeaderBytes: 32 << 10, ResponseHeaderTimeout: 20 * time.Second, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	return siteRelayTransport(input, output, transport)
}
func siteRelayTransport(input io.Reader, output io.Writer, transport http.RoundTripper) error {
	d := json.NewDecoder(io.LimitReader(input, 2<<20))
	d.DisallowUnknownFields()
	var in hosting.RelayRequest
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || len(in.Body) > 1<<20 {
		return errors.New("invalid relay request")
	}
	u, err := url.ParseRequestURI(in.Path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" || len(in.Path) > 8192 {
		return errors.New("invalid relay path")
	}
	switch in.Method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return errors.New("invalid relay method")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, in.Method, "http://127.0.0.1:8080"+in.Path, bytes.NewReader(in.Body))
	if err != nil {
		return errors.New("invalid relay request")
	}
	for _, key := range []string{"Accept", "Content-Type", "Cookie", "If-None-Match", "If-Modified-Since"} {
		for _, v := range in.Header.Values(key) {
			req.Header.Add(key, v)
		}
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("application unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(body) > 8<<20 {
		return errors.New("application response limit")
	}
	headers := http.Header{}
	for _, key := range []string{"Content-Type", "ETag", "Last-Modified", "Location", "Set-Cookie"} {
		for _, v := range response.Header.Values(key) {
			headers.Add(key, v)
		}
	}
	return json.NewEncoder(output).Encode(hosting.RelayResponse{Status: response.StatusCode, Header: headers, Body: body})
}
func siteGo(args []string) error {
	if len(args) != 1 {
		return errors.New("site expiry required")
	}
	expiry, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || time.Until(time.Unix(expiry, 0)) <= 0 || time.Until(time.Unix(expiry, 0)) > 30*24*time.Hour {
		return errors.New("invalid site expiry")
	}
	if os.Getpid() != 1 {
		return errors.New("site supervisor must be pid1")
	}
	if err := protectSiteSupervisor(); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(expiry, 0))
	defer cancel()
	// Wall-clock jumps must not extend a fleet lease beyond its hard expiry.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if !now.Before(time.Unix(expiry, 0)) {
					cancel()
					return
				}
			}
		}
	}()
	command := exec.Command("/work/app")
	command.Dir = "/work"
	command.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp", "PORT=8080", "HOST=127.0.0.1"}
	// Never persist or echo untrusted application output.
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return runSiteProcess(ctx, command)
}
func siteCanary() error {
	if os.Getuid() == 0 {
		return errors.New("root site")
	}
	for _, p := range []string{"/state", "/run/secrets", "/var/run/docker.sock"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			return errors.New("unexpected site mount")
		}
	}
	if file, err := os.OpenFile("/proc/1/mem", os.O_WRONLY, 0); err == nil {
		_ = file.Close()
		return errors.New("unprotected site supervisor")
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return errors.New("network canary failed")
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback == 0 {
			return errors.New("unexpected network interface")
		}
	}
	if f, err := os.Create("/work/.hosting-canary"); err == nil {
		_ = f.Close()
		_ = os.Remove("/work/.hosting-canary")
		return errors.New("writable release")
	}
	return nil
}
