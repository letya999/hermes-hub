package api

import (
	"strings"
)

func PublicPrepareError(err error) string {
	if err == nil {
		return ""
	}
	text := secretValuePattern.ReplaceAllString(err.Error(), "[redacted]")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 400 {
		text = text[:400]
	}
	return text
}
