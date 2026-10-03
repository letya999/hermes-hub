package mediasvc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// materialize resolves one audio source to a local file inside dir. Sources:
// an upload already written by the caller, a path under InputRoot (mounted
// user file storage), or an https URL — presigned S3/GCS links work since the
// fetch is a plain GET. Remote fetch is allowlist-gated and private IP ranges
// are refused so the endpoint cannot be turned into an SSRF proxy.
func (s *service) materialize(ctx context.Context, sourceURL, sourcePath, dir string) (string, error) {
	switch {
	case strings.TrimSpace(sourceURL) != "":
		return s.fetchURL(ctx, strings.TrimSpace(sourceURL), dir)
	case strings.TrimSpace(sourcePath) != "":
		return s.resolveInput(sourcePath)
	}
	return "", errors.New("source_url or source_path is required")
}

func (s *service) resolveInput(sourcePath string) (string, error) {
	if s.cfg.InputRoot == "" {
		return "", errors.New("local file sources are not configured")
	}
	if filepath.IsAbs(sourcePath) || len(sourcePath) > 1024 || strings.ContainsRune(sourcePath, 0) {
		return "", errors.New("invalid source path")
	}
	root, err := filepath.EvalSymlinks(s.cfg.InputRoot)
	if err != nil {
		return "", errors.New("input root unavailable")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(sourcePath)))
	if err != nil || !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return "", errors.New("source path outside the input root")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("source not found")
	}
	if info.Size() == 0 || info.Size() > s.cfg.MaxSource {
		return "", errors.New("source outside size bounds")
	}
	return resolved, nil
}

func (s *service) fetchURL(ctx context.Context, raw, dir string) (string, error) {
	if len(s.cfg.FetchHosts) == 0 {
		return "", errors.New("remote fetch is not configured")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", errors.New("only https sources are accepted")
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, h := range s.cfg.FetchHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("host %q is not in the fetch allowlist", host)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	client := &http.Client{
		Timeout: 30 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				h, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", h)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() {
						return nil, fmt.Errorf("refused private or non-routable address for %s", h)
					}
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return s.saveFetchBody(resp, u, dir)
}

// saveFetchBody streams an OK response into a fresh file under dir, bounded
// by MaxSource — split out so the disk side of fetch is testable without a
// routable address.
func (s *service) saveFetchBody(resp *http.Response, u *url.URL, dir string) (string, error) {
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("source fetch returned %d", resp.StatusCode)
	}
	dest := filepath.Join(dir, "source"+extensionFromResponse(resp, u))
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, s.cfg.MaxSource+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	if n == 0 || n > s.cfg.MaxSource {
		_ = os.Remove(dest)
		return "", errors.New("source outside size bounds")
	}
	return dest, nil
}

func extensionFromResponse(resp *http.Response, u *url.URL) string {
	ext := strings.ToLower(filepath.Ext(u.Path))
	if ext == "" || len(ext) > 8 {
		ext = ".bin"
	}
	return ext
}

// decodeAudio converts any ffmpeg-readable input into 16 kHz mono PCM wav —
// the single format every STT engine consumes.
func decodeAudio(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-y", "-i", src, "-vn", "-ac", "1", "-ar", "16000", "-f", "wav", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("audio decode failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
