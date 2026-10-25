package communication

import "testing"

func TestGroupCannotBorrowSlackPrivateIdentity(t *testing.T) {
	c := groupConfig(t)
	c.Users[0].SlackIDs = []SlackLink{{TeamID: "T1", UserID: "U1"}}
	if err := c.Validate(); err == nil {
		t.Error("group home also bound to a personal Slack account")
	}
}
