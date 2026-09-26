package toolhub

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// TrustedBuilderPolicy is the only accepted builder bootstrap. It is not
	// the MCP runtime profile: SETUID/SETGID and systempaths=unconfined exist
	// solely so rootless BuildKit can call newuidmap/newgidmap. Privileged
	// and seccomp=unconfined workers stay forbidden.
	TrustedBuilderPolicy            = "rootless-buildkit-bootstrap-v1"
	restrictedBuildKitImage         = "moby/buildkit@sha256:93efd7f1c17f16cea080c34f68e19863b9fe541db550bf607221ce425c0ab9ef"
	restrictedBuildProxyImage       = "ghcr.io/stacklok/toolhive/egress-proxy@sha256:72a43857af69e602bdc1285bbb074e829ae967c1ede4144377ddeb048bacb1be"
	restrictedSBOMScannerImage      = "docker/buildkit-syft-scanner@sha256:ae4f3b554449e7e25548e7d8ccc029d17357348e30c6e3df01b92bc93654d6a9"
	restrictedBuildKitSeccompSHA256 = "bd1e45460cc93f0cb19393e58c48c8f33b2e410f537d94ac16ba2283bfd982ef"
)

func builderArgsAreTrustedBootstrap(args []string) bool {
	joined := strings.Join(args, "\x00")
	return strings.Contains(joined, "SETUID") && strings.Contains(joined, "SETGID") && strings.Contains(joined, "systempaths=unconfined") && strings.Contains(joined, "--oci-worker-no-process-sandbox") && !contains(args, "--privileged") && !strings.Contains(joined, "seccomp=unconfined")
}

var restrictedBuildHosts = []string{
	"auth.docker.io", "registry-1.docker.io", "production.cloudflare.docker.com", "production.cloudfront.docker.com",
	"pypi.org", "files.pythonhosted.org",
	"registry.npmjs.org",
	"proxy.golang.org", "sum.golang.org", "storage.googleapis.com",
	"index.crates.io", "static.crates.io", "static.rust-lang.org",
}

// restrictedBuildProxyConfig permits only TLS package downloads needed by the
// four MVP recipes. The builder remains on an internal Docker network and has
// no direct route; only this proxy sidecar joins an external network.
func restrictedBuildProxyConfig() string {
	return "http_port 3128\n" +
		"acl tlsport port 443\n" +
		"acl packages dstdomain " + strings.Join(restrictedBuildHosts, " ") + "\n" +
		"http_access allow tlsport packages\n" +
		"http_access deny all\n" +
		"cache deny all\n" +
		"access_log none\n" +
		"cache_log /dev/stderr\n" +
		"pid_filename /tmp/squid.pid\n"
}

// restrictedBuildKitCreateArgs returns the only builder bootstrap profile that
// Hermes accepts. It is intentionally separate from the stricter MCP runtime
// profile: SETUID/SETGID and selected namespace syscalls are needed only by
// rootless BuildKit during bootstrap.
func restrictedBuildKitCreateArgs(name, stateVolume, seccompPath, network, proxyName, contextVolume, outputVolume string) ([]string, error) {
	if !toolNamePattern.MatchString(name) || !toolNamePattern.MatchString(stateVolume) || !filepath.IsAbs(seccompPath) || ((network == "") != (proxyName == "")) || ((contextVolume == "") != (outputVolume == "")) || (network != "" && (!toolNamePattern.MatchString(network) || !toolNamePattern.MatchString(proxyName))) || (contextVolume != "" && (!toolNamePattern.MatchString(contextVolume) || !toolNamePattern.MatchString(outputVolume))) {
		return nil, fmt.Errorf("%w: fixed builder name, volume and absolute seccomp path required", ErrInvalid)
	}
	seccompPath = filepath.Clean(seccompPath)
	if err := noSymlinkPath(seccompPath); err != nil {
		return nil, err
	}
	profile, err := os.ReadFile(seccompPath) // #nosec G304 -- operator path is absolute, symlink-free and content-pinned below.
	if err != nil || len(profile) > 64<<10 || fmt.Sprintf("%x", sha256.Sum256(profile)) != restrictedBuildKitSeccompSHA256 {
		return nil, fmt.Errorf("%w: unapproved builder seccomp profile", ErrInvalid)
	}
	args := []string{
		"create", "--name", name,
		"--label", "hermes-hub.role=artifact-builder",
		"--user", "1000:1000",
		"--read-only",
		"--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID",
		"--security-opt", "seccomp=" + seccompPath,
		"--security-opt", "systempaths=unconfined",
		"--cpus", "2", "--memory", "2g", "--memory-swap", "2g", "--pids-limit", "512",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
		"--tmpfs", "/run/user/1000:rw,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=16m",
		"--tmpfs", "/home/user/.local/tmp:rw,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=256m",
		"--tmpfs", "/home/user/.docker:rw,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=1m",
		"--mount", "type=volume,source=" + stateVolume + ",target=/home/user/.local/share/buildkit",
	}
	if contextVolume != "" {
		args = append(args,
			"--mount", "type=volume,source="+contextVolume+",target=/run/hermes-context,readonly",
			"--mount", "type=volume,source="+outputVolume+",target=/run/hermes-output")
	}
	if network == "" {
		args = append(args, "--network", "none")
	} else {
		proxy := "http://" + proxyName + ":3128"
		args = append(args, "--network", network,
			"--env", "HTTP_PROXY="+proxy, "--env", "HTTPS_PROXY="+proxy, "--env", "NO_PROXY=",
			"--env", "http_proxy="+proxy, "--env", "https_proxy="+proxy, "--env", "no_proxy=")
	}
	// The shared state volume has no TTL: bound buildkitd GC so repeated
	// artifact builds cannot grow it without limit (4 GiB keeps the warm
	// base-image cache useful without eating the host disk).
	return append(args, restrictedBuildKitImage, "--oci-worker-no-process-sandbox", "--oci-worker-snapshotter=native", "--oci-worker-gc-keepbytes=4294967296"), nil
}

func restrictedBuildProxyCreateArgs(name, network, configVolume string) ([]string, error) {
	if !toolNamePattern.MatchString(name) || !toolNamePattern.MatchString(network) || !toolNamePattern.MatchString(configVolume) {
		return nil, fmt.Errorf("%w: fixed build proxy identity required", ErrInvalid)
	}
	return []string{
		"create", "--name", name,
		"--label", "hermes-hub.role=artifact-build-egress",
		"--user", "squid", "--network", network, "--read-only",
		"--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID",
		"--security-opt", "no-new-privileges",
		"--cpus", "0.25", "--memory", "256m", "--memory-swap", "256m", "--pids-limit", "64",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--tmpfs", "/run:rw,nosuid,nodev,size=16m",
		"--mount", "type=volume,source=" + configVolume + ",target=/run/hermes-config,readonly",
		restrictedBuildProxyImage, "-N", "-d", "1", "-f", "/run/hermes-config/squid.conf",
	}, nil
}
