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

func EnvOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
