package communication

import "testing"

func TestGroupConfigurationRejectsPersonalAuthority(t *testing.T) {
	for _, mode := range []string{"personal", "missing", "alias", "positive-chat", "bad-member", "duplicate-member", "duplicate-chat", "shared-space"} {
		t.Run(mode, func(t *testing.T) {
			c := testConfig(t)
			c.Users = append(c.Users, User{ID: "team", Enabled: true, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
			c.Groups = []Group{{ChatID: -100123, Space: "team", Members: []int64{11}, Mention: "@helper"}}
			switch mode {
			case "personal":
				c.Groups[0].Space = "alice"
			case "missing":
				c.Groups[0].Space = "missing"
			case "alias":
				c.Users[len(c.Users)-1].RuntimeID = "alice"
			case "positive-chat":
				c.Groups[0].ChatID = 11
			case "bad-member":
				c.Groups[0].Members = []int64{-1}
			case "duplicate-member":
				c.Groups[0].Members = []int64{11, 11}
			case "duplicate-chat":
				c.Groups = append(c.Groups, c.Groups[0])
			case "shared-space":
				other := c.Groups[0]
				other.ChatID = -100456
				c.Groups = append(c.Groups, other)
			}
			if err := c.Validate(); err == nil {
				t.Fatalf("unsafe group mapping accepted: %s", mode)
			}
		})
	}
}
