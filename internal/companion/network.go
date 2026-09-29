package companion

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Network transports are the last-resort path for MCP servers that cannot
// speak stdio. The child keeps its declared listener; the bridge discovers it
// inside the workload's own network namespace and connects as an MCP client.
// The listener is only ever dialed on loopback, and the HTTP client never
// honors proxy environment: traffic between a companion and its child must
// stay inside the workload, not detour through the egress proxy.
const (
	networkConnectTimeout = 4 * time.Second
	networkPollInterval   = 400 * time.Millisecond
)

var networkHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		// Connect's ctx owns the established stream's lifetime, so per-attempt
		// bounds live here instead: a listener that accepts TCP but never
		// answers HTTP still fails within networkConnectTimeout and the next
		// candidate is tried.
		ResponseHeaderTimeout: networkConnectTimeout,
		ForceAttemptHTTP2:     false,
	},
}

type networkProbe struct {
	Kind     string
	Port     int
	Path     string
	Wait     time.Duration
	Exclude  map[int]bool
	onListen func(port int)
}

// connectNetworkChild spawns a network-transport server and connects to it as
// an MCP client. The child inherits stdout/stderr for log visibility; its
// stdin is left closed. An early child exit fails the wait immediately so a
// crashed server does not burn the whole discovery budget.
func connectNetworkChild(ctx context.Context, client *mcp.Client, cmd *exec.Cmd, c Config) (*mcp.ClientSession, error) {
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
		case exitErr := <-exited:
			cancel()
			_ = exitErr
		}
	}()
	conn, err := connectNetwork(ctx, networkProbe{
		Kind: c.Transport,
		Port: c.NetworkPort,
		Path: c.NetworkPath,
	})
	if err != nil {
		select {
		case exitErr := <-exited:
			return nil, fmt.Errorf("network child exited before connect: %v", exitErr)
		default:
		}
		return nil, err
	}
	session, err := client.Connect(ctx, &staticTransport{conn: conn}, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return session, nil
}

// staticTransport adapts an already-established connection to the Transport
// interface so client.Connect can drive the MCP handshake over a discovered
// network endpoint.
type staticTransport struct {
	conn mcp.Connection
}

func (t *staticTransport) Connect(context.Context) (mcp.Connection, error) {
	return t.conn, nil
}

// connectNetwork loops until a network transport succeeds or the wait budget
// is spent. Ports are discovered from the kernel's listen table so a server
// that binds an unadvertised port still connects.
func connectNetwork(ctx context.Context, probe networkProbe) (mcp.Connection, error) {
	if probe.Wait <= 0 {
		probe.Wait = 30 * time.Second
	}
	deadline := time.Now().Add(probe.Wait)
	var lastErr error
	for {
		ports := candidateListenPorts(probe.Port, probe.Exclude)
		for _, port := range ports {
			for _, path := range candidateEndpointPaths(probe.Path) {
				for _, transport := range networkTransports(probe.Kind, port, path) {
					conn, err := transport.Connect(ctx)
					if err == nil {
						if probe.onListen != nil {
							probe.onListen(port)
						}
						return conn, nil
					}
					lastErr = err
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("network transport connect failed (%v) after %s", lastErr, probe.Wait)
			}
			return nil, fmt.Errorf("no loopback listener discovered after %s", probe.Wait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(networkPollInterval):
		}
	}
}

func networkTransports(kind string, port int, path string) []mcp.Transport {
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port) + path
	var transports []mcp.Transport
	add := func(name string) {
		switch name {
		case "sse":
			transports = append(transports, &mcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: networkHTTPClient})
		case "streamable-http", "http":
			transports = append(transports, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: networkHTTPClient, MaxRetries: 1})
		}
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "auto":
		add("sse")
		add("streamable-http")
	default:
		add(kind)
	}
	return transports
}

func candidateEndpointPaths(declared string) []string {
	if declared != "" {
		return []string{declared}
	}
	return []string{"/sse", "/mcp", "/events", "/"}
}

// candidateListenPorts reads the kernel TCP listen table in this network
// namespace. A declared port wins; otherwise every loopback/wildcard listener
// is a candidate (the workload's own bridge listener is excluded by name).
func candidateListenPorts(declared int, exclude map[int]bool) []int {
	if declared > 0 {
		return []int{declared}
	}
	var ports []int
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		ports = append(ports, procNetListenPorts(file, exclude)...)
	}
	return ports
}

// procNetListenPorts parses /proc/net/tcp{,6} and returns ports in LISTEN
// state bound to loopback or wildcard addresses. Listeners bound only to a
// specific non-loopback IP are unreachable via 127.0.0.1 and skipped.
func procNetListenPorts(file string, exclude map[int]bool) []int {
	body, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var ports []int
	seen := map[int]bool{}
	lines := strings.Split(string(body), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "0A" {
			continue
		}
		hostHex, portHex, found := strings.Cut(fields[1], ":")
		if !found || !loopbackOrWildcardHex(hostHex) {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 || seen[int(port)] || exclude[int(port)] {
			continue
		}
		seen[int(port)] = true
		ports = append(ports, int(port))
	}
	return ports
}

func loopbackOrWildcardHex(hostHex string) bool {
	switch len(hostHex) {
	case 8: // IPv4: 0100007F loopback, 00000000 wildcard
		return hostHex == "0100007F" || hostHex == "00000000"
	case 32: // IPv6: all-zeros wildcard, or ::1 loopback
		return hostHex == "00000000000000000000000000000000" || hostHex == "00000000000000000000000000000001"
	}
	return false
}
