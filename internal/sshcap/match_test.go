package sshcap

import "testing"

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		{"uptime", "uptime", true},
		{"uptime", "uptimex", false},
		{"systemctl status *", "systemctl status nginx", true},
		{"systemctl status *", "status nginx", false},
		{"*", "anything at all", true},
		{"/var/log/*", "/var/log/app/x.log", true},
		{"/var/log/*", "/etc/passwd", false},
		{"*.log", "a.log", true},
		{"*.log", "a.logx", false},
		{"a*c*z", "abcz", true},
		{"a*c*z", "acb", false},
		{"a**b", "axb", true},
		{"", "x", false},
		{"docker ps", "docker ps -a", false},
	} {
		if got := match(c.pattern, c.s); got != c.want {
			t.Fatalf("match(%q,%q)=%v want %v", c.pattern, c.s, got, c.want)
		}
	}
}
