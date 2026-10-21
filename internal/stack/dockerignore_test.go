package stack

import (
	"os"
	"strings"
	"testing"
)

func TestDockerContextExcludesLocalInternalArtifacts(t *testing.T) {
	body, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	include, exclude := -1, -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "!internal/**":
			include = i
		case "internal/.local/":
			exclude = i
		}
	}
	if include < 0 || exclude <= include {
		t.Fatal("Docker context must exclude internal/.local/ after including internal sources")
	}
}
