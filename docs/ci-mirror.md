---
description: Repository mirroring, portable GitLab CI publication and optional Helm deployment.
last_verified: 2026-11-17
---

# Mirror and publish

Create an empty private GitLab project. Copy the source with a temporary bare
clone and `git push --mirror <destination>`; that command replaces destination
refs, so use it only for the new empty project. For continued synchronization,
configure a one-way repository mirror with a dedicated, narrowly scoped credential.
Keep one authoritative source, protect the destination default branch and disable
publication from untrusted refs. Do not commit mirror credentials or real spaces.

The root `.gitlab-ci.yml` uses a trusted, disposable Linux **shell** runner with
Bash, Git, Go from `go.mod`, just 1.57.0, Docker Engine with Compose/Buildx,
Helm 3.19.0, kubectl, Node 26/npm, Python 3 and a C
compiler. Install the SQLCipher development codec and expose its library as
`libsqlite3` for the broker's existing `libsqlite3` build tag, as in the source CI.
Do not weaken that gate by substituting ordinary SQLite. Install CLI plugins
system-wide so an isolated `DOCKER_CONFIG` can still discover them. Restrict the
runner to trusted code: Docker daemon access grants control of its host.

Set these project CI variables; use protected, masked values for credentials:

| Variable | Value |
|---|---|
| `HUB_CI_RUNNER_TAG` | Tag of the prepared runner |
| `HUB_REGISTRY_HOST` | Registry authority, optionally `host:port`; no scheme or credentials |
| `HUB_REGISTRY_REPO` | Lowercase repository namespace/path, without host or trailing slash |
| `HUB_REGISTRY_USER`, `HUB_REGISTRY_PASSWORD` | Robot account with push access to this namespace |
| `HUB_ENV` | Environment slug; default `dev` |
| `HUB_DEPLOY_ENABLED` | Default `false`; set `true` to expose the manual deploy job |
| `HUB_KUBECONFIG` | Protected **file** variable containing a least-privilege kubeconfig |
| `HUB_KUBE_CONTEXT` | Explicit context in that kubeconfig |
| `HUB_HELM_RELEASE`, `HUB_HELM_NAMESPACE` | Target release and namespace |
| `HUB_HELM_VALUES_FILE` | Protected **file** variable containing reviewed chart overrides and Secret references |

`test-job` runs the existing format/docs/vet, race/shuffle coverage (85% overall,
75% per internal package), lint, deployment configuration, Helm, broker, dependency
security and secret-scanning gates. Any failure prevents image publication.
Five matrix builds on the protected default branch publish
`$HUB_REGISTRY_HOST/$HUB_REGISTRY_REPO/<image>:$CI_COMMIT_SHA` and `:edge-$HUB_ENV`:
`hermes-hub-control`, `hermes-hub-runtime`, `cli-tools`, `cell-proxy`,
`credential-broker`. The same production image targets serve every environment;
environment selection belongs to chart values. OCI revision labels record HEAD.

Configure the registry to reject replacement of forty-hex SHA tags and retain
them through the rollback window; permit moving `edge-*`. A retry can be rejected
by this policy rather than replace an existing SHA. Resource groups serialize
writers per image/environment. Cancel superseded pipelines before retrying older
ones: moving tags describe the last successful publisher. Deploy uses the selected
pipeline's SHA, so an older moving tag cannot silently change that release.

Prepare Secret references, image pull credentials and persistent state as described
in [operations](operations.md#kubernetes-control-plane). Run the manual deploy job
to execute `helm upgrade --install --reset-values --atomic --wait`. The job maps
the four hub services to the control image and the broker to its own image, clears
stale digest overrides, and pins the supervisor runtime to the same source SHA.
Private registry access uses a pre-created Kubernetes image pull Secret selected
by `imagePullSecrets` in the values file. Keep actual service secrets out of Helm
values and release metadata. CA trust for the runner and cluster is an operator
prerequisite; production TLS verification stays enabled.

For a cloud-free smoke test, create an isolated `registry:2` container bound to
loopback, then follow the [kind local registry recipe](https://kind.sigs.k8s.io/docs/user/local-registry/)
to connect it to the kind network and configure the node's registry alias. Use a
separate kubeconfig and only synthetic Secrets/state. Publish with
`go run ./cmd/devcheck docker-publish-to localhost:5001/check/<image> <target> dev`
for each matrix target. Point the chart at those SHA refs with `IfNotPresent`;
do not preload application images with `kind load`. Install, check Pod image
digests and authenticated status/metrics, then upgrade and check persistence.
The loopback-only anonymous HTTP registry is a local fixture. Delete that cluster
and registry afterward, and run `just docker-clean` after local image builds.
This smoke does not establish real channel delivery or spawned runtime Pods;
the chart's remaining boundaries are recorded in the operations runbook.

Validate the imported configuration with the target instance's CI Lint before
enabling credentials. Local contracts exercise the deploy script and Helm
renderer; only an actual pipeline run establishes GitLab/runner acceptance.
