package communication

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/letya999/hermes-hub/internal/channel"
	"strconv"
	"time"
)

func limitTelegramText(text string) string { return channel.LimitTelegramText(text) }

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}
