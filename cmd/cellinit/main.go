// cellinit is the static helper the controller bind-mounts into every CLI
// cell at /cellinit. It is the cell's pid1 and canary agent; keeping the
// contract in one tiny binary means cells need no shell and scratch or
// distroless tool images work unchanged.
//
//	pause                       pid1: reap orphans, block until SIGTERM/SIGINT
//	write <path> [content]      stdin or argv -> file (0600); canary seeding
//	check <path> <token>        exit 0 iff file content equals token
//	clean <dir> <name> <token>  exit 0 iff dir holds only <name> == token
//	procs                       exit 0 iff no stray processes exist; list them
//	sweep                       SIGKILL every process except pid1 and self
//	cred-proxy                  HTTP forward proxy injecting brokered headers
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fail("cellinit: subcommand required")
	}
	var err error
	switch os.Args[1] {
	case "pause":
		err = pause()
	case "write":
		err = writeFile(os.Args[2:])
	case "check":
		err = check(os.Args[2:])
	case "clean":
		err = clean(os.Args[2:])
	case "procs":
		err = procs()
	case "sweep":
		err = sweep()
	case "cred-proxy":
		err = credProxy(os.Args[2:])
	default:
		err = fmt.Errorf("cellinit: unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func pause() error {
	go reapOrphans()
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	return nil
}

func writeFile(args []string) error {
	var data []byte
	switch len(args) {
	case 1:
		var err error
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			return err
		}
	case 2:
		data = []byte(args[1])
	default:
		return fmt.Errorf("write: want <path> [content]")
	}
	return os.WriteFile(filepath.Clean(args[0]), data, 0o600)
}

// strayPIDs lists every pid that is neither pid1 nor the caller. Exec'd tool
// children and their orphans are all visible in the cell's own pid namespace.
func strayPIDs() ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	self := os.Getpid()
	var stray []string
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == 1 || pid == self {
			continue
		}
		stray = append(stray, entry.Name())
	}
	return stray, nil
}

func procs() error {
	stray, err := strayPIDs()
	if err != nil {
		return err
	}
	if len(stray) != 0 {
		return fmt.Errorf("stray processes: %s", strings.Join(stray, ","))
	}
	return nil
}

func sweep() error {
	stray, err := strayPIDs()
	if err != nil {
		return err
	}
	for _, pid := range stray {
		id, _ := strconv.Atoi(pid)
		killProc(id)
	}
	return nil
}

func check(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("check: want <path> <token>")
	}
	data, err := os.ReadFile(filepath.Clean(args[0])) // #nosec G304 -- path is operator-chosen.
	if err != nil {
		return err
	}
	if string(data) != args[1] {
		return fmt.Errorf("canary token mismatch")
	}
	return nil
}

// clean verifies <dir> contains exactly the canary file <name> with content
// <token> — no tool leftovers, no foreign markers.
func clean(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("clean: want <dir> <name> <token>")
	}
	entries, err := os.ReadDir(filepath.Clean(args[0]))
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != args[1] || entries[0].IsDir() {
		return fmt.Errorf("canary: %d entries, want only %s", len(entries), args[1])
	}
	return check([]string{filepath.Join(args[0], args[1]), args[2]})
}

// credProxy is a plain (non-CONNECT) forward proxy: the cell's tool calls
// http://credproxy:<port>/<upstream-path> with Host already set or taken from
// the first path segment. Only hosts in the rules file are proxied; each rule
// injects fixed headers (the brokered credential). Rules arrive on a mounted
// file — never through argv or env — so `docker inspect` of the proxy and the
// tool cell never exposes a secret.
func credProxy(args []string) error {
	var rulesPath, listen string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--rules" && i+1 < len(args):
			rulesPath = args[i+1]
			i++
		case args[i] == "--listen" && i+1 < len(args):
			listen = args[i+1]
			i++
		}
	}
	if rulesPath == "" {
		return fmt.Errorf("cred-proxy: --rules required")
	}
	if listen == "" {
		listen = ":3128"
	}
	rules, err := readProxyRules(rulesPath)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           &proxyHandler{rules: rules},
		ReadHeaderTimeout: 10 * time.Second,
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		_ = server.Shutdown(context.Background())
	}()
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

type proxyRule struct {
	Headers map[string]string `json:"headers"`
	Scheme  string            `json:"scheme,omitempty"`
}

type proxyHandler struct {
	rules map[string]proxyRule
}

func readProxyRules(path string) (map[string]proxyRule, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- operator config.
	if err != nil {
		return nil, err
	}
	var raw map[string]map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	rules := map[string]proxyRule{}
	for host, headers := range raw {
		rules[strings.ToLower(host)] = proxyRule{Headers: headers, Scheme: "https"}
	}
	return rules, nil
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Hostname()
	if host == "" {
		// Cell-side requests use http://credproxy/<host>/<path>; the first path
		// segment carries the upstream when the URL is relative.
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		host = parts[0]
		r.URL.Path = "/"
		if len(parts) == 2 {
			r.URL.Path = "/" + parts[1]
		}
	}
	rule, ok := h.rules[strings.ToLower(host)]
	if !ok {
		http.Error(w, "cred-proxy: host not allowed", http.StatusForbidden)
		return
	}
	scheme := rule.Scheme
	if scheme == "" {
		scheme = "https"
	}
	target := *r.URL
	target.Scheme = scheme
	target.Host = host
	out := r.Clone(r.Context())
	out.URL = &target
	out.RequestURI = ""
	out.Host = host
	for k, v := range rule.Headers {
		out.Header.Set(k, v)
	}
	resp, err := (&http.Transport{
		DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
	}).RoundTrip(out)
	if err != nil {
		http.Error(w, "cred-proxy: upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 8<<20))
}
