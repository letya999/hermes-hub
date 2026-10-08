package toolhub

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreparedProbeProxyIsolationAndCleanup(t *testing.T) {
	var commands []string
	run := func(_ context.Context, args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if args[0] == "inspect" {
			return []byte("172.20.0.2\n"), nil
		}
		return nil, nil
	}
	network, proxyURL, stop, err := startPreparedProbeProxyWithDocker(t.Context(), []string{"example.com"}, run)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL != "http://172.20.0.2:3128" || !strings.Contains(strings.Join(commands, "\n"), "network create --internal") {
		t.Fatalf("probe has no isolated network: %q %v", proxyURL, commands)
	}
	stop()
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "network rm "+network) || !strings.Contains(joined, "rm -f "+strings.TrimSuffix(network, "-net")+"-proxy") || !strings.Contains(joined, "volume rm "+strings.TrimSuffix(network, "-net")+"-config") {
		t.Fatalf("probe resources were not removed: %v", commands)
	}
}

func TestPreparedProbeProxyRejectsInvalidAddress(t *testing.T) {
	var cleaned bool
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return []byte("8.8.8.8"), nil
		}
		if args[0] == "rm" {
			cleaned = true
		}
		return nil, nil
	}
	_, _, _, err := startPreparedProbeProxyWithDocker(t.Context(), []string{"example.com"}, run)
	if !errors.Is(err, ErrIsolation) || !cleaned {
		t.Fatalf("public proxy address was accepted or not cleaned up: %v", err)
	}
}

func TestPreparedProbeProxyCleansUpFailedDockerStep(t *testing.T) {
	for _, failed := range []string{"network create", "volume create", "create --name", "cp ", "network connect", "start ", "inspect "} {
		t.Run(failed, func(t *testing.T) {
			var cleaned bool
			run := func(_ context.Context, args ...string) ([]byte, error) {
				command := strings.Join(args, " ")
				if strings.HasPrefix(command, failed) {
					return nil, errors.New("docker unavailable")
				}
				if args[0] == "rm" {
					cleaned = true
				}
				return nil, nil
			}
			_, _, _, err := startPreparedProbeProxyWithDocker(t.Context(), []string{"example.com"}, run)
			if !errors.Is(err, ErrIsolation) || !cleaned {
				t.Fatalf("%s: failure did not deny and clean up: %v", failed, err)
			}
		})
	}
}
