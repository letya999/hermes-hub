import json, os, hashlib, sys

CORPUS = sys.argv[1]

def entry(claim, source, ref, rank, **kw):
    e = {"claim": claim, "source": source, "ref": ref, "rank": rank}
    e.update(kw)
    return e

def field(name, required=None, secret=None, delivery="env", target=""):
    c = {"field": name, "delivery": delivery}
    if required is not None: c["required"] = required
    if secret is not None: c["secret"] = secret
    if target: c["target"] = target
    return c

def attr(name, value):
    return {"attribute": name, "value": json.dumps(value)}

T = []
T.append({
 "target": "github/github-mcp-server",
 "subject": {"kind": "mcp-remote", "ref": "github/github-mcp-server@sha256:"+"a1"*64},
 "provenance": ["https://raw.githubusercontent.com/github/github-mcp-server/main/server.json"],
 "entries": [
   entry(attr("endpoint", "https://api.githubcopilot.com/mcp/"), "server.json", "server.json@repository-root", "L4"),
   entry(field("GITHUB_PERSONAL_ACCESS_TOKEN", required=False, secret=True), "server.json", "server.json@packages[0].runtimeArguments", "R3"),
   entry(field("Authorization", required=True, secret=True, delivery="http_header", target="Authorization"), "server.json", "server.json@remotes[0].headers", "R3"),
 ]})
T.append({
 "target": "zereight/gitlab-mcp",
 "subject": {"kind": "mcp-container", "ref": "zereight/gitlab-mcp@sha256:"+"b2"*64},
 "provenance": ["https://raw.githubusercontent.com/zereight/gitlab-mcp/main/server.json"],
 "entries": [
   entry(attr("artifact", "docker.io/zereight050/gitlab-mcp:2.3.3"), "server.json", "server.json@packages[1]", "L4"),
   entry(field("GITLAB_PERSONAL_ACCESS_TOKEN", required=False, secret=True), "server.json", "server.json@environmentVariables", "R3"),
   entry(field("GITLAB_PERSONAL_ACCESS_TOKEN_FILE", required=False, secret=False, delivery="file"), "server.json", "server.json@environmentVariables", "R3"),
   entry(field("GITLAB_API_URL", required=False, secret=False), "server.json", "server.json@environmentVariables", "R3"),
   entry(field("GITLAB_USE_OAUTH", required=False, secret=False), "server.json", "server.json@environmentVariables", "R3"),
 ]})
T.append({
 "target": "nspady/google-calendar-mcp",
 "subject": {"kind": "mcp-container", "ref": "nspady/google-calendar-mcp@sha256:"+"c3"*64},
 "provenance": ["https://raw.githubusercontent.com/nspady/google-calendar-mcp/main/package.json",
                "https://raw.githubusercontent.com/nspady/google-calendar-mcp/main/.env.example"],
 "entries": [
   entry(attr("entrypoint", ["node", "build/index.js"]), "package.json", "package.json@bin", "L6"),
   entry(field("GOOGLE_OAUTH_CREDENTIALS", required=True, secret=False, delivery="file"), ".env.example", ".env.example", "R6"),
   entry(field("GOOGLE_CALENDAR_MCP_TOKEN_PATH", required=False, secret=False, delivery="file"), ".env.example", ".env.example", "R6"),
   entry(field("TRANSPORT", required=False, secret=False), ".env.example", ".env.example", "R6"),
 ]})
T.append({
 "target": "makenotion/notion-mcp-server",
 "subject": {"kind": "mcp-container", "ref": "makenotion/notion-mcp-server@sha256:"+"d4"*64},
 "provenance": ["https://raw.githubusercontent.com/makenotion/notion-mcp-server/main/package.json",
                "https://raw.githubusercontent.com/makenotion/notion-mcp-server/main/README.md"],
 "entries": [
   entry(attr("entrypoint", ["npx", "-y", "@notionhq/notion-mcp-server"]), "README.md", "README.md@mcpServers", "hint"),
   entry(field("NOTION_TOKEN", required=True, secret=True), "README.md", "README.md@env", "R6"),
   entry(field("OPENAPI_MCP_HEADERS", required=False, secret=True, delivery="json"), "README.md", "README.md@env", "R6"),
 ]})
T.append({
 "target": "sooperset/mcp-atlassian",
 "subject": {"kind": "mcp-container", "ref": "sooperset/mcp-atlassian@sha256:"+"e5"*64},
 "provenance": ["https://raw.githubusercontent.com/sooperset/mcp-atlassian/main/Dockerfile",
                "https://raw.githubusercontent.com/sooperset/mcp-atlassian/main/.env.example"],
 "entries": [
   entry(attr("entrypoint", ["mcp-atlassian"]), "Dockerfile", "Dockerfile@ENTRYPOINT", "hint"),
   entry(field("JIRA_URL", required=True, secret=False), ".env.example", ".env.example", "R6"),
   entry(field("CONFLUENCE_URL", required=True, secret=False), ".env.example", ".env.example", "R6"),
   entry(field("JIRA_API_TOKEN", required=False, secret=True), ".env.example", ".env.example", "R6"),
   entry(field("CONFLUENCE_API_TOKEN", required=False, secret=True), ".env.example", ".env.example", "R6"),
   entry(field("ATLASSIAN_OAUTH_CLIENT_ID", required=False, secret=False), ".env.example", ".env.example", "R6"),
 ]})
T.append({
 "target": "registry-remote-only/example-remote",
 "subject": {"kind": "mcp-remote", "ref": "acme/remote-mcp@sha256:"+"f6"*64},
 "provenance": ["recorded MCP Registry document shape: remotes[0].url only, no packages"],
 "entries": [
   entry(attr("endpoint", "https://mcp.acme.example/v1"), "mcp-registry", "registry.modelcontextprotocol.io/acme/remote-mcp", "L3"),
   entry(field("ACME_API_KEY", required=True, secret=True, delivery="http_header", target="Authorization"), "mcp-registry", "registry.modelcontextprotocol.io/acme/remote-mcp", "R2"),
 ]})
T.append({
 "target": "anthropics/filesystem-mcpb",
 "subject": {"kind": "mcp-container", "ref": "anthropics/filesystem-mcpb@sha256:"+"a7"*64},
 "provenance": ["MCPB manifest.json user_config declarations"],
 "entries": [
   entry(attr("entrypoint", ["node", "dist/index.js", "${user_config.allowed_dir}"]), "manifest.json", "bundle/manifest.json", "L4"),
   entry(field("allowed_dir", required=True, secret=False), "manifest.json", "manifest.json@user_config.allowed_dir", "R3"),
 ]})
T.append({
 "target": "docker/mcp-notion",
 "subject": {"kind": "mcp-container", "ref": "makenotion/notion-mcp-server@sha256:"+"d4"*64},
 "provenance": ["Docker MCP catalog entry: image mcp/notion + secrets contract"],
 "entries": [
   entry(attr("artifact", "mcp/notion@sha256:"+"11"*64), "docker-mcp", "desktop.docker.com/mcp/catalog/notion", "L2"),
   entry(field("NOTION_TOKEN", required=True, secret=True), "docker-mcp", "desktop.docker.com/mcp/catalog/notion", "R4"),
 ]})
for name, cmd in [("cli/gh", ["gh"]), ("cli/jq", ["jq"]), ("cli/kubectl", ["kubectl"])]:
    T.append({
     "target": name,
     "subject": {"kind": "cli", "ref": name + "@sha256:" + hashlib.sha256(cmd[0].encode()).hexdigest()},
     "provenance": ["bounded-cli recipe: language-convention entrypoint"],
     "entries": [entry(attr("entrypoint", cmd), "--help", name, "L6")]})
misc = [
  ("modelcontextprotocol/filesystem", [("FS_ALLOWED_DIRS", True, False)], ["node","dist/index.js"]),
  ("modelcontextprotocol/memory",     [("MEMORY_FILE_PATH", False, False)], ["node","dist/index.js"]),
  ("modelcontextprotocol/fetch",      [], ["uvx","mcp-server-fetch"]),
  ("modelcontextprotocol/git",        [("GIT_REPOSITORY", False, False)], ["uvx","mcp-server-git"]),
  ("modelcontextprotocol/postgres",   [("POSTGRES_URL", True, True)], ["node","dist/index.js"]),
  ("modelcontextprotocol/sqlite",     [("SQLITE_DB_PATH", True, False)], ["uvx","mcp-server-sqlite"]),
  ("modelcontextprotocol/brave-search",[("BRAVE_API_KEY", True, True)], ["node","dist/index.js"]),
  ("modelcontextprotocol/slack",      [("SLACK_BOT_TOKEN", True, True), ("SLACK_TEAM_ID", True, False)], ["node","dist/index.js"]),
  ("modelcontextprotocol/puppeteer",  [], ["node","dist/index.js"]),
  ("modelcontextprotocol/time",       [("TIME_LOCAL_TIMEZONE", False, False)], ["uvx","mcp-server-time"]),
]
for target, fields, entrypoint in misc:
    ref = "sha256:" + hashlib.sha256(target.encode()).hexdigest()
    entries = [entry(attr("entrypoint", entrypoint), "README.md", "README.md@mcpServers", "hint")]
    for name, req, sec in fields:
        entries.append(entry(field(name, required=req, secret=sec), "README.md", "README.md@env", "R6"))
    T.append({"target": target, "subject": {"kind": "mcp-container", "ref": target+"@"+ref},
              "provenance": ["recorded upstream README/env contract"], "entries": entries})

os.makedirs(CORPUS, exist_ok=True)
for item in T:
    slug = item["target"].replace("/", "__")
    with open(os.path.join(CORPUS, slug + ".json"), "w", newline="\n") as f:
        json.dump(item, f, indent=2)
        f.write("\n")
print(len(T), "corpus entries written")
