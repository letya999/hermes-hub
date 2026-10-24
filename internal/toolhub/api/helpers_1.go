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

// CredentialStagingRoot selects where per-call credential material
// (credentials.env, <name>.secret, runtime env) is staged before the
// controller hand-off. The deploy mounts it on tmpfs so secrets never reach
// the persistent host disk; HUB_CRED_STAGING unset keeps the legacy state
// root for dev and tests.
func CredentialStagingRoot() string {
	if staging := strings.TrimSpace(os.Getenv("HUB_CRED_STAGING")); staging != "" {
		return staging
	}
	return EnvOr("HUB_STATE", "/state")
}
