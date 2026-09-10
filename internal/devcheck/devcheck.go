package devcheck

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"go/format"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func Coverage(name string, minimum float64) (float64, error) {
	f, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() || !regexp.MustCompile(`^mode: (set|count|atomic)$`).MatchString(s.Text()) {
		return 0, fmt.Errorf("invalid coverage header")
	}
	var total, covered int
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 3 {
			return 0, fmt.Errorf("invalid coverage row %q", s.Text())
		}
		statements, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || statements < 0 || count < 0 {
			return 0, fmt.Errorf("invalid coverage row %q", s.Text())
		}
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	if err := s.Err(); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, fmt.Errorf("empty coverage profile")
	}
	actual := float64(covered) * 100 / float64(total)
	if actual < minimum {
		return actual, fmt.Errorf("go coverage %.2f%% < %.2f%%", actual, minimum)
	}
	return actual, nil
}

func Project(root string) error {
	required := []string{"AGENTS.md", "README.md", "SETUP.md", "CONTRIBUTING.md", "SECURITY.md", "LICENSE", "docs/index.md", "specs/index.md", ".work/index.md"}
	var problems []string
	for _, name := range required {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil || !info.Mode().IsRegular() {
			problems = append(problems, "missing "+name)
		}
	}
	link := regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)
	for _, folder := range []string{"docs", "specs", ".work"} {
		err := filepath.WalkDir(filepath.Join(root, folder), func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(name) != ".md" {
				return nil
			}
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, name)
			if bytes.Contains(body, []byte("{{")) {
				problems = append(problems, "template placeholder: "+rel)
			}
			if folder == "docs" && !bytes.HasPrefix(body, []byte("---\ndescription:")) && !bytes.HasPrefix(body, []byte("---\r\ndescription:")) {
				problems = append(problems, "missing metadata: "+rel)
			}
			for _, match := range link.FindAllSubmatch(body, -1) {
				target := string(match[1])
				if parsed, err := url.Parse(target); err != nil || parsed.IsAbs() || strings.HasPrefix(target, "#") {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				if _, err := os.Stat(filepath.Join(filepath.Dir(name), filepath.FromSlash(target))); err != nil {
					problems = append(problems, "broken link: "+rel+" -> "+target)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

func Format(root string) error {
	var bad []string
	for _, folder := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, folder), func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(name) != ".go" {
				return err
			}
			body, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			formatted, err := format.Source(body)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if !bytes.Equal(body, formatted) {
				rel, _ := filepath.Rel(root, name)
				bad = append(bad, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("gofmt required: %s", strings.Join(bad, ", "))
	}
	return nil
}

func DockerSmoke(ctx context.Context, image string) error {
	var suffix [5]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("hermes-smoke-%x", suffix)
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return cmd.Run()
	}
	defer exec.Command("docker", "rm", "-f", name).Run()
	checks := [][]string{
		{"run", "--rm", "--entrypoint", "hermes", image, "--version"},
		{"run", "--rm", "--entrypoint", "glab", image, "--version"},
		{"run", "--rm", "--entrypoint", "/opt/hermes/.venv/bin/python", image, "-c", "import sqlite3, playwright, faster_whisper; assert sqlite3.sqlite_version_info >= (3,53,4)"},
		{"run", "--rm", "--entrypoint", "/opt/google/.venv/bin/python", image, "-c", "import fastmcp, google.auth"},
		{"run", "--rm", "--entrypoint", "/opt/telegram/.venv/bin/python", image, "-c", "import telethon, mcp"},
		{"run", "-d", "--name", name, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--shm-size", "1g", "--tmpfs", "/tmp:mode=1777", "--tmpfs", "/state:uid=10001,gid=10001,mode=0700", "--tmpfs", "/workspace:uid=10001,gid=10001,mode=0700", "-e", "HUB_BROWSER=true", image, "idle"},
	}
	for _, args := range checks {
		if err := run(args...); err != nil {
			return err
		}
	}
	for range 90 {
		if exec.CommandContext(ctx, "docker", "exec", name, "hub-runtime", "health").Run() == nil {
			fmt.Println("Standalone Docker runtime smoke passed; no live provider calls")
			return nil
		}
		time.Sleep(time.Second)
	}
	_ = run("logs", name)
	return fmt.Errorf("agent/browser failed health check")
}
