//go:build integration

package toolhub

// #287 acceptance fixture: a workload carrying a `proxy`-delivered credential
// reaches its declared API through the cred sidecar. The fixture TLS server
// must observe the real Authorization header while `docker inspect`,
// /proc/1/environ and the workload filesystem carry only the `hub-managed`
// stub. The CA is generated in-test and reaches the sidecar tmpfs by docker
// cp (SSL_CERT_FILE), mirroring how rules.json arrives — fixture-only
// plumbing, the production path talks to real public TLS endpoints.
//
//	HUB_CLI_TOOLS_IMAGE=hermes-hub-cli-tools:test go test -tags integration -run TestCredProxyFixtureRealHeader ./internal/toolhub
//
// Requires: docker daemon, the cli-tools image, a Linux-capable `go` to
// cross-build cellinit, and Docker Desktop's host.docker.internal resolver.
import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const credProxyFixtureHost = "host.docker.internal"

func fixtureCAPEM(t *testing.T) (caPEM, serverPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTPL := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "credproxy fixture CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(2 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTPL, caTPL, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	srvKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	srvTPL := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: credProxyFixtureHost},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(2 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{credProxyFixtureHost},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTPL, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srvDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: mustEC(t, srvKey)})
	return caPEM, serverPEM, keyPEM
}

func mustEC(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestCredProxyFixtureRealHeader(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tools := api.EnvOr("HUB_CLI_TOOLS_IMAGE", "hermes-hub-cli-tools:test")
	if out, err := api.LocalCommand(ctx, "docker", "image", "inspect", tools); err != nil {
		t.Skipf("image %s unavailable: %v (%s)", tools, err, out)
	}

	secret := "real-secret-fixture-4f2a91"
	gotAuth := make(chan string, 1)
	caPEM, serverPEM, keyPEM := fixtureCAPEM(t)
	pair, err := tls.X509KeyPair(serverPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "0.0.0.0:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = fixture.Serve(listener) }()
	t.Cleanup(func() { _ = fixture.Shutdown(context.Background()) })
	fixturePort := listener.Addr().(*net.TCPAddr).Port
	upstreamHost := fmt.Sprintf("%s:%d", credProxyFixtureHost, fixturePort)

	root := t.TempDir()
	cellinit := filepath.Join(root, "cellinit")
	build := exec.CommandContext(ctx, "go", "build", "-o", cellinit, "github.com/letya999/hermes-hub/cmd/cellinit")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-build cellinit: %v (%s)", err, out)
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0600); err != nil {
		t.Fatal(err)
	}
	rules, err := json.Marshal(map[string]map[string]string{
		upstreamHost: {"Authorization": "Bearer " + secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	rulesFile := filepath.Join(root, "rules.json")
	if err := os.WriteFile(rulesFile, rules, 0600); err != nil {
		t.Fatal(err)
	}
	caFile := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}

	workload := "fix-wl"
	sidecar := workload + "-cred"
	network := "hermes-" + workload
	docker := func(args ...string) []byte {
		out, derr := api.LocalCommand(ctx, "docker", args...)
		if derr != nil {
			t.Fatalf("docker %s: %v (%s)", strings.Join(args, " "), derr, out)
		}
		return out
	}
	cleanup := func() {
		_, _ = api.LocalCommand(context.Background(), "docker", "rm", "-f", workload, sidecar)
		_, _ = api.LocalCommand(context.Background(), "docker", "network", "rm", network)
	}
	cleanup() // drop leftovers from an interrupted run
	t.Cleanup(cleanup)

	docker("network", "create", "--internal", network)
	// Same sidecar contract as cellCredProxy, plus SSL_CERT_FILE so the
	// fixture's private CA is trusted; the env carries a path, never a secret.
	docker("create", "--name", sidecar, "--network", network, "--read-only",
		"--user", "10001:10001", "--cap-drop", "ALL",
		"--security-opt", "no-new-privileges=true", "--security-opt", "seccomp="+seccomp,
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
		"--mount", "type=bind,source="+cellinit+",target=/cellinit,readonly",
		"-e", "SSL_CERT_FILE=/tmp/ca.pem",
		"--entrypoint", "/cellinit", tools, "cred-proxy", "--rules", "/tmp/rules.json",
		"--listen", "0.0.0.0:3129")
	docker("network", "connect", "bridge", sidecar)
	docker("start", sidecar)
	// Rules and the fixture CA arrive through exec stdin, same channel as the
	// controller — the read-only rootfs refuses docker cp, and a host temp
	// file would leave the credential on disk.
	writeToSidecar := func(src, dst string) {
		cmd := exec.CommandContext(ctx, "docker", "exec", "-i", sidecar, "/cellinit", "write", dst)
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			t.Fatal(rerr)
		}
		cmd.Stdin = bytes.NewReader(data)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("exec write %s: %v (%s)", dst, err, out)
		}
	}
	writeToSidecar(rulesFile, "/tmp/rules.json")
	writeToSidecar(caFile, "/tmp/ca.pem")

	// The workload sees only the stub credential and the rewritten endpoint —
	// this is what ProxyStubPlan + proxyGrantEnvArgs produce at spawn. The
	// image entrypoint is /cellinit, so sleep needs an explicit entrypoint.
	docker("run", "-d", "--name", workload, "--network", network,
		"-e", "API_TOKEN="+api.ProxyStubValue,
		"-e", "FIXTURE_API="+sidecar+":3129/"+upstreamHost,
		"-e", "NO_PROXY=127.0.0.1,"+sidecar,
		"--entrypoint", "/bin/sleep", tools, "120")

	// Wait for the proxy listener, then ask the workload to call its API.
	// The tools image has no curl/wget/python — bash's /dev/tcp does the
	// plain-HTTP hop workload->sidecar.
	out, err := api.LocalCommand(ctx, "docker", "exec", sidecar, "sh", "-c",
		"i=0; while [ $i -lt 50 ]; do grep -q ':0C39' /proc/net/tcp /proc/net/tcp6 2>/dev/null && exit 0; i=$((i+1)); sleep 0.2; done; exit 1")
	if err != nil {
		t.Fatalf("cred-proxy never listened: %v (%s)", err, out)
	}
	resp, err := api.LocalCommand(ctx, "docker", "exec", workload, "/bin/bash", "-c",
		fmt.Sprintf(`exec 3<>/dev/tcp/%s/3129; printf 'GET /%s/check HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n' >&3; head -1 <&3`, sidecar, upstreamHost))
	if err != nil {
		t.Fatalf("workload call failed: %v (%s)", err, resp)
	}
	if !strings.Contains(string(resp), "200") {
		t.Fatalf("fixture status: %s", resp)
	}
	select {
	case auth := <-gotAuth:
		if auth != "Bearer "+secret {
			t.Fatalf("fixture saw %q", auth)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("fixture never received a request")
	}

	// inspect and environ of both containers must carry no real value.
	for _, name := range []string{workload, sidecar} {
		inspect := docker("inspect", name)
		if strings.Contains(string(inspect), secret) {
			t.Fatalf("docker inspect %s leaks the credential", name)
		}
		environ, err := api.LocalCommand(ctx, "docker", "exec", name, "sh", "-c", "cat /proc/1/environ | tr '\\0' '\\n'")
		if err != nil {
			t.Fatalf("environ %s: %v", name, err)
		}
		if strings.Contains(string(environ), secret) {
			t.Fatalf("/proc/1/environ of %s leaks the credential", name)
		}
	}
	if out := docker("exec", workload, "sh", "-c", "echo $API_TOKEN"); !strings.Contains(string(out), api.ProxyStubValue) {
		t.Fatalf("workload env is not the stub: %s", out)
	}
	// The workload filesystem must hold no rules file at all; the sidecar's
	// /tmp is tmpfs by construction (verified by inspect mounts above).
	fs, err := api.LocalCommand(ctx, "docker", "exec", workload, "sh", "-c", "find /tmp /work /home -type f 2>/dev/null | xargs grep -l . 2>/dev/null")
	if err == nil && strings.Contains(string(fs), "rules") {
		t.Fatalf("workload filesystem carries proxy material: %s", fs)
	}
}
