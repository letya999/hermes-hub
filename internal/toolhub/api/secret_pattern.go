package api

import (
	"regexp"
)

var secretValuePattern = regexp.MustCompile(`(?i)\b(xox[baprs]-[A-Za-z0-9-]{8,}|sk-[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{15,}|[A-Fa-f0-9]{32,})\b`)
