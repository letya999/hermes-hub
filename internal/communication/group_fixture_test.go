package communication

import "testing"

func groupConfig(t *testing.T) Config {
	c := testConfig(t)
	c.Users[0].TelegramIDs = nil
	c.Groups = []Group{{ChatID: -100123, Space: c.Users[0].ID, Members: []int64{11}, Mention: "@helper"}}
	return c
}
