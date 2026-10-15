package api

import (
	"regexp"
)

var secretValuePattern = regexp.MustCompile(`(?i)\b(xox[baprs]-[A-Za-z0-9-]{8,}|sk-[A-Za-z0-9]{16,}|[A-Fa-f0-9]{32,})\b`)
