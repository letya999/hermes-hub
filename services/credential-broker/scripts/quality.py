#!/usr/bin/env python3
"""Small, offline-capable quality backend. Human/CI entrypoint: just recipes."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / ".build" / "quality"
MODULE = "github.com/letya999/credential-broker/"


def run(args: list[str], *, capture: bool = False, env: dict[str, str] | None = None) -> str:
    print("+ " + " ".join(args), flush=True)
    child_env = os.environ.copy()
    child_env["GOTOOLCHAIN"] = "local"
    if env:
        child_env.update(env)
    result = subprocess.run(args, cwd=ROOT, env=child_env, text=True,
                            stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.STDOUT if capture else None, check=False)
    text = result.stdout or ""
    if result.returncode:
        if text:
            print(text, file=sys.stderr)
        raise SystemExit(result.returncode)
    return text


def fmt(check: bool) -> None:
    paths = sorted(str(p.relative_to(ROOT)) for p in ROOT.rglob("*.go")
                   if ".tools" not in p.parts and ".build" not in p.parts)
    output = run(["gofmt", "-l" if check else "-w", *paths], capture=True)
    if check and output.strip():
        raise SystemExit("Run just fmt:\n" + output)


def lint() -> None:
    fmt(True)
    run(["go", "vet", "./..."])
    run([sys.executable, "scripts/openapi.py", "--check"])
    run(["go", "test", "./internal/archtest", "-count=1"])
    # No credentials, private key files or accidentally checked-in runtime state.
    bad = []
    for path in ROOT.rglob("*"):
        if not path.is_file() or any(p in {".git", ".build", ".tools", "__pycache__"} for p in path.parts):
            continue
        if path.suffix in {".private", ".key", ".p12", ".pfx"} or path.name in {".env", "credentials.env", "ledger.bin", "runtime_session.json"}:
            bad.append(str(path.relative_to(ROOT)))
        if path.suffix in {".md", ".go", ".json", ".yaml", ".yml", ".py", ".toml"}:
            text = path.read_text(encoding="utf-8")
            if re.search(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----\r?\n[A-Za-z0-9+/]{32,}", text):
                bad.append(str(path.relative_to(ROOT)) + ": private key block")
            if re.search(r"\bgh[pousr]_[A-Za-z0-9]{30,}\b", text):
                bad.append(str(path.relative_to(ROOT)) + ": GitHub token pattern")
    if bad:
        raise SystemExit("Secret hygiene failed:\n" + "\n".join(bad))
    print("Secret hygiene and architecture checks passed (not a full DLP scanner).")


def coverage(path: Path) -> dict:
    # -coverpkg=./... emits repeated blocks for different test binaries.
    # Merge identical source blocks; never inflate denominators or coverage.
    blocks: dict[str, tuple[int, int]] = {}
    for line in path.read_text(encoding="utf-8").splitlines()[1:]:
        location, statements, count = line.split()
        n, hits = int(statements), int(count)
        old = blocks.get(location)
        if old and old[0] != n:
            raise SystemExit("Inconsistent coverage profile block: " + location)
        blocks[location] = (n, hits + (old[1] if old else 0))
    packages: dict[str, list[int]] = {}
    for location, (n, hits) in blocks.items():
        package = location.rsplit("/", 1)[0].removeprefix(MODULE)
        pair = packages.setdefault(package, [0, 0])
        pair[0] += n
        pair[1] += n if hits else 0
    rows = {name: {"statements": total, "covered": covered,
                   "percent": round(100 * covered / total, 2)}
            for name, (total, covered) in sorted(packages.items()) if total}
    total = sum(v["statements"] for v in rows.values())
    covered = sum(v["covered"] for v in rows.values())
    result = {"metric": "Go statement coverage; merged source blocks; no exclusions",
              "minimum_total_percent": 85, "minimum_each_package_percent": 85,
              "total": {"statements": total, "covered": covered,
                        "percent": round(100 * covered / total, 2)}, "packages": rows}
    OUTPUT.mkdir(parents=True, exist_ok=True)
    (OUTPUT / "coverage.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))
    failed = [name for name, row in rows.items() if row["covered"] * 100 < row["statements"] * 85]
    if not total or covered * 100 < total * 85 or failed:
        raise SystemExit("Coverage gate failed: " + ", ".join(failed))
    return result


def race() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    log = run(["go", "test", "-json", "-race", "-count=1", "-timeout=120s", "-covermode=atomic",
               "-coverpkg=./...", "-coverprofile=" + str(OUTPUT / "coverage.out"), "./..."], capture=True)
    (OUTPUT / "tests.jsonl").write_text(log, encoding="utf-8")
    functions = run(["go", "tool", "cover", "-func=" + str(OUTPUT / "coverage.out")], capture=True)
    (OUTPUT / "coverage-functions.txt").write_text(functions, encoding="utf-8")
    coverage(OUTPUT / "coverage.out")


def fuzz() -> None:
    OUTPUT.mkdir(parents=True, exist_ok=True)
    logs = []
    for package, name in [("./identity", "FuzzVerifier"), ("./contract", "FuzzPaths"),
                          ("./internal/strictjson", "FuzzDecode")]:
        logs.append(run(["go", "test", package, "-run=^$", "-fuzz=^" + name + "$",
                         "-fuzztime=3s", "-parallel=2"], capture=True))
    (OUTPUT / "fuzz.txt").write_text("\n".join(logs), encoding="utf-8")
    print("Fuzz smoke campaigns passed; see .build/quality/fuzz.txt")


def build() -> None:
    dest = ROOT / ".build" / "bin"
    dest.mkdir(parents=True, exist_ok=True)
    for name in ["credential-broker", "brokerctl"]:
        run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w",
             "-o", str(dest / name), "./cmd/" + name], env={"CGO_ENABLED": "0"})
    print(run([str(dest / "credential-broker"), "version"], capture=True).strip())


def external_tools() -> None:
    versions = json.loads((ROOT / "tools.json").read_text())
    if run(["go", "env", "GOVERSION"], capture=True).strip() != versions["go_production"]:
        raise SystemExit("Production toolchain must match tools.json; review version updates explicitly.")
    for tool, args in [("staticcheck", ["./..."]), ("gosec", ["-quiet", "./..."]),
                       ("govulncheck", ["./..."])]:
        binary = ROOT / ".tools" / "bin" / tool
        if not binary.exists():
            raise SystemExit("Missing security tool; run just tools-install: " + tool)
        run([str(binary), *args])


def install_tools() -> None:
    versions = json.loads((ROOT / "tools.json").read_text())
    dest = ROOT / ".tools" / "bin"
    dest.mkdir(parents=True, exist_ok=True)
    for item in versions["go_tools"].values():
        run(["go", "install", item], env={"GOBIN": str(dest)})


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("task", choices=["fmt", "fmt-check", "lint", "test", "test-unit", "test-integration",
                                        "test-e2e", "test-race", "coverage", "fuzz", "build", "check",
                                        "tools-install", "lint-security", "release-check", "doctor"])
    args = parser.parse_args()
    unit = ["./identity", "./contract", "./internal/...", "./materialize", "./client"]
    integration = ["./broker", "./provider", "./httpapi", "./app", "./cmd/..."]
    if args.task == "fmt": fmt(False)
    elif args.task == "fmt-check": fmt(True)
    elif args.task == "lint": lint()
    elif args.task in {"test", "test-unit", "test-integration", "test-e2e"}:
        paths = {"test": ["./..."], "test-unit": unit, "test-integration": integration,
                 "test-e2e": ["./tests/e2e"]}[args.task]
        run(["go", "test", "-count=1", "-timeout=120s", *paths])
    elif args.task == "test-race": race()
    elif args.task == "coverage": coverage(OUTPUT / "coverage.out")
    elif args.task == "fuzz": fuzz()
    elif args.task == "build": build()
    elif args.task == "tools-install": install_tools()
    elif args.task == "lint-security": external_tools()
    elif args.task in {"check", "release-check"}:
        lint()
        run([sys.executable, "docs_scripts/check.py", "check"])
        race()
        build()
        if args.task == "release-check":
            fuzz()
            external_tools()
    elif args.task == "doctor":
        print(json.dumps({"platform": sys.platform, "python": sys.version,
                          "go": shutil.which("go"), "just": shutil.which("just"),
                          "external_security_tools": {x: (ROOT / ".tools/bin" / x).exists()
                                                      for x in ["gosec", "govulncheck", "staticcheck"]}}, indent=2))

if __name__ == "__main__":
    main()
