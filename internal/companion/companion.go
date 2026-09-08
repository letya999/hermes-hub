// Package companion bridges a trusted native stdio MCP tool server to private HTTP.
package companion

import (
	"context"
	"crypto/subtle"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

type Config struct {
	Listen       string   `yaml:"listen"`
	TokenEnv     string   `yaml:"token_env"`
	Command      []string `yaml:"command"`
	AllowedTools []string `yaml:"allowed_tools"`
}

func Protect(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser origins forbidden", http.StatusForbidden)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
		next.ServeHTTP(w, r)
	})
}
func Run(ctx context.Context, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var c Config
	d := yaml.NewDecoder(strings.NewReader(string(b)))
	d.KnownFields(true)
	if err = d.Decode(&c); err != nil {
		return err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8765"
	}
	token := os.Getenv(c.TokenEnv)
	if len(token) < 32 || len(c.Command) == 0 {
		return fmt.Errorf("command and token_env containing >=32 characters required")
	}
	cmd := exec.CommandContext(ctx, c.Command[0], c.Command[1:]...)
	cmd.Stderr = os.Stderr
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, c.TokenEnv+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hub-companion", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "hub-companion", Version: "0.1.0"}, nil)
	count := 0
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		if len(c.AllowedTools) > 0 && !slices.Contains(c.AllowedTools, t.Name) {
			continue
		}
		name := t.Name
		server.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: r.Params.Arguments})
		})
		count++
	}
	if count == 0 {
		return fmt.Errorf("no allowed tools discovered")
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", Protect(handler, token))
	srv := &http.Server{Addr: c.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()
	fmt.Fprintf(os.Stderr, "companion listening on %s (%d tools); use a private tunnel\n", c.Listen, count)
	if err = srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
