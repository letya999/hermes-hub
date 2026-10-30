// Package gitsource installs skills from tracked git mirrors (SPEC-0083 R10):
// restricted HTTPS sources, per-owner bare mirrors under skills/.sources,
// commit-pinned trees and broker-leased credentials inside bounded cells.
package gitsource

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

var ErrInvalid = errors.New("invalid skill source")

// Source is the canonical form of an accepted repository URL (R10.1).
// Slug names the requested skill when the locator came from a package
// registry page (skills.sh) rather than a git URL — it resolves against
// discovered candidates instead of addressing a repo subtree.
type Source struct {
	Repo string `json:"repo"` // canonical https clone URL
	Ref  string `json:"ref,omitempty"`
	Path string `json:"path,omitempty"`
	Slug string `json:"slug,omitempty"`
}

// ParseSource validates raw into a canonical Source.
func ParseSource(raw, gitlabHost string) (Source, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Source{}, ErrInvalid
	}
	host := strings.ToLower(u.Host)
	// skills.sh is a package registry, not a git host: its pages name a
	// skill slug under an owner/repo pair that lives on GitHub. The pair
	// re-validates through the github.com branch below; the remaining
	// segments become the requested skill slug.
	if host == "www.skills.sh" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return Source{}, ErrInvalid
		}
		src, err := ParseSource("https://github.com/"+parts[0]+"/"+parts[1], gitlabHost)
		if err != nil {
			return Source{}, ErrInvalid
		}
		src.Slug = strings.Join(parts[2:], "/")
		if !validSubfolder(src.Slug) {
			return Source{}, ErrInvalid
		}
		return src, nil
	}
	if host != "github.com" && host != "gitlab.com" && host != strings.ToLower(gitlabHost) {
		return Source{}, ErrInvalid
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	var source Source
	if host != "github.com" {
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] != "-" {
				continue
			}
			source.Ref, source.Path = parts[i+2], strings.Join(parts[i+3:], "/")
			parts = parts[:i]
			break
		}
	} else if len(parts) > 2 {
		if len(parts) < 4 {
			return Source{}, ErrInvalid
		}
		source.Ref, source.Path = parts[3], strings.Join(parts[4:], "/")
		parts = parts[:2]
	}
	if len(parts) < 2 {
		return Source{}, ErrInvalid
	}
	for _, part := range parts {
		if part == "" || strings.Contains(part, "..") {
			return Source{}, ErrInvalid
		}
	}
	if strings.HasSuffix(parts[len(parts)-1], ".git") {
		return Source{}, ErrInvalid
	}
	if strings.HasPrefix(source.Ref, "-") || !validSubfolder(source.Path) {
		return Source{}, ErrInvalid
	}
	source.Repo = "https://" + host + "/" + strings.Join(parts, "/")
	return source, nil
}

// validSubfolder requires a clean relative path with no traversal or
// credential-shaped segments.
func validSubfolder(value string) bool {
	if value == "" {
		return true
	}
	if path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		lower := strings.ToLower(part)
		if part == "" || part == ".git" || strings.Contains(lower, "secret") {
			return false
		}
	}
	return true
}
