package communication

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

func contains(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}

func limitTelegramText(text string) string {
	runes := []rune(text)
	if len(runes) <= 4000 {
		return text
	}
	return string(runes[:4000])
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
