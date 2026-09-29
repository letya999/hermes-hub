package stack

import (
	"fmt"
	"net/url"
	"strings"
)

// MCPServer is an owner-configured upstream connection, not an embedded service.
type MCPServer struct {
	URL     string            `yaml:"url,omitempty"`
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Tools   *MCPTools         `yaml:"tools,omitempty"`
	Auth    string            `yaml:"auth,omitempty"`
	Timeout int               `yaml:"timeout,omitempty"`
}

type MCPTools struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

func validateMCP(servers map[string]MCPServer) error {
	reserved := map[string]bool{"hub": true, "browser": true, "browser_guest": true, "google": true, "telegram_user": true, "slack": true, "github": true, "atlassian": true, "desktop": true, "drafts": true}
	for name, s := range servers {
		if !idPattern.MatchString(name) || reserved[name] {
			return fmt.Errorf("invalid or reserved MCP name %q", name)
		}
		if (s.URL == "") == (s.Command == "") {
			return fmt.Errorf("MCP %s requires exactly one of url/command", name)
		}
		if s.Timeout < 0 || s.Timeout > 600 {
			return fmt.Errorf("MCP timeout must be 0..600")
		}
		if s.Auth != "" && s.Auth != "oauth" {
			return fmt.Errorf("MCP auth must be oauth or empty")
		}
		if s.URL != "" {
			u, e := url.Parse(s.URL)
			if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid MCP URL")
			}
			if len(s.Args) > 0 || len(s.Env) > 0 {
				return fmt.Errorf("remote MCP cannot have stdio args/env")
			}
		} else if len(s.Headers) > 0 || s.Auth != "" {
			return fmt.Errorf("stdio MCP cannot have HTTP headers/auth")
		}
		for _, v := range s.Headers {
			if strings.ContainsAny(v, "\r\n") {
				return fmt.Errorf("invalid MCP header")
			}
		}
		if s.Tools != nil && len(s.Tools.Include) == 0 && len(s.Tools.Exclude) == 0 {
			return fmt.Errorf("MCP tools filter cannot be empty")
		}
	}
	return nil
}
func (s MCPServer) Config() M {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 120
	}
	if s.URL != "" {
		m := M{"url": s.URL, "timeout": timeout, "skip_preflight": true}
		if len(s.Headers) > 0 {
			m["headers"] = s.Headers
		}
		if s.Auth != "" {
			m["auth"] = s.Auth
		}
		if s.Tools != nil {
			m["tools"] = s.Tools
		}
		return m
	}
	m := M{"command": s.Command, "args": s.Args, "env": s.Env, "timeout": timeout}
	if s.Tools != nil {
		m["tools"] = s.Tools
	}
	return m
}
