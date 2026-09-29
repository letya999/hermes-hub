package companion

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var forwardEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// TCPForwardConfig declares a loopback listener relayed through the workload
// HTTP CONNECT proxy to one bound host:port destination held in the named
// environment variable. Destinations live outside tool arguments and the
// egress ACL already bounds what CONNECT may reach.
type TCPForwardConfig struct {
	Listen    int    `yaml:"listen" json:"listen"`
	TargetEnv string `yaml:"target_env" json:"target_env"`
}

// startTCPForwards validates the declared forwards and opens the active ones.
// A forward whose target variable is unset stays closed: the database driver
// then fails fast on connection refused instead of timing out on a mystery.
func startTCPForwards(ctx context.Context, forwards []TCPForwardConfig) error {
	if len(forwards) == 0 {
		return nil
	}
	if len(forwards) > 8 {
		return fmt.Errorf("too many tcp forwards")
	}
	seenListen, seenEnv := map[int]bool{}, map[string]bool{}
	for _, forward := range forwards {
		if forward.Listen < 1024 || forward.Listen > 65535 || seenListen[forward.Listen] || !forwardEnvPattern.MatchString(forward.TargetEnv) || seenEnv[forward.TargetEnv] {
			return fmt.Errorf("invalid tcp forward")
		}
		seenListen[forward.Listen], seenEnv[forward.TargetEnv] = true, true
	}
	for _, forward := range forwards {
		target := strings.TrimSpace(os.Getenv(forward.TargetEnv))
		if target == "" {
			fmt.Fprintf(os.Stderr, "companion tcp forward %d inactive: %s unset\n", forward.Listen, forward.TargetEnv)
			continue
		}
		host, port, err := net.SplitHostPort(target)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("tcp forward target %s must be host:port", forward.TargetEnv)
		}
		if portNum, err := strconv.Atoi(port); err != nil || portNum < 1 || portNum > 65535 {
			return fmt.Errorf("tcp forward target %s must be host:port", forward.TargetEnv)
		}
		proxy := forwardProxyURL()
		if proxy == "" {
			return fmt.Errorf("tcp forwards require an HTTP CONNECT proxy environment")
		}
		listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(forward.Listen))
		if err != nil {
			return fmt.Errorf("tcp forward %d: %w", forward.Listen, err)
		}
		go serveTCPForward(ctx, listener, proxy, target)
		fmt.Fprintf(os.Stderr, "companion tcp forward 127.0.0.1:%d -> %s\n", forward.Listen, target)
	}
	return nil
}

func forwardProxyURL() string {
	if proxy := os.Getenv("HTTPS_PROXY"); proxy != "" {
		return proxy
	}
	return os.Getenv("HTTP_PROXY")
}

func serveTCPForward(ctx context.Context, listener net.Listener, proxy, target string) {
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			upstream, err := connectViaProxy(ctx, proxy, target)
			if err != nil {
				return
			}
			defer upstream.Close()
			splice(conn, upstream)
		}()
	}
}

// bufferedConn wraps the CONNECT tunnel: its buffered reader can already
// hold bytes the server pushed right after the 200 response (for example a
// database greeting), so reads must go through it.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// connectViaProxy issues CONNECT host:port to the reviewed egress proxy and
// returns the tunnel only for an affirmative response. The handshake is
// bounded; the returned connection carries no deadline.
func connectViaProxy(ctx context.Context, proxy, target string) (net.Conn, error) {
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return nil, fmt.Errorf("unsupported proxy url")
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp4", parsed.Host)
	if err != nil {
		return nil, err
	}
	if err := raw.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		raw.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: hermes-companion\r\n\r\n", target, target); err != nil {
		raw.Close()
		return nil, err
	}
	reader := bufio.NewReaderSize(raw, 4096)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		raw.Close()
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw.Close()
		return nil, fmt.Errorf("proxy refused connect: %s", response.Status)
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		raw.Close()
		return nil, err
	}
	return &bufferedConn{Conn: raw, reader: reader}, nil
}

// splice copies in both directions and returns when either side ends; the
// caller closes both connections.
func splice(client net.Conn, upstream net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
}
