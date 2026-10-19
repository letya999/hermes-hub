package api

import (
	"net"
	"os"
	"strings"
)

func FormOriginForListen(listen string) string {
	listen = strings.TrimRight(strings.TrimSpace(listen), "/")
	if strings.Contains(listen, "://") {
		return listen
	}
	host, port, err := net.SplitHostPort(listen)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::" || host == "[::]") {
		return "http://127.0.0.1:" + port
	}
	if listen == "" {
		return "http://127.0.0.1"
	}
	return "http://" + listen
}

// PublicOrigin is the opt-in operator-owned surface for human action links
// (HUB_PUBLIC_ORIGIN). Only https qualifies — a misconfigured http origin
// is ignored so links never downgrade to plaintext.
func PublicOrigin() string {
	value := strings.TrimRight(strings.TrimSpace(os.Getenv("HUB_PUBLIC_ORIGIN")), "/")
	if strings.HasPrefix(value, "https://") {
		return value
	}
	return ""
}

func EnvOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
