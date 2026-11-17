# CHG-0318: registry-agnostic GitLab CI (M9 #351)

## Goal

Run the existing gates from a mirrored repository, publish five source-addressed
images to a CI-selected registry, and optionally deploy the Helm control plane.
No customer identifiers, registry product assumptions or embedded credentials.

## Read before coding

README.md, docs/index.md, docs/status-hygiene.md, docs/operations.md, justfile,
.github/workflows/ci.yml, Docker registry helpers and Dockerfile, Helm chart;
issue #351. Owner authorizes implementation and local registry push/kind tests.
No account login, remote mirror creation, Git push or external publication.

## Slices

1. Extend the existing Go publisher with an explicit qualified image destination,
   SHA-only immutable tag and moving environment tag; preserve existing callers.
2. Add five-image GitLab matrix behind the existing gates; protected default branch
   publishing, isolated Docker login config, explicit manual Helm deployment.
3. Add a dedicated broker image using the existing static SQLCipher binary and
   shell required by the chart's private Secret materializer.
4. Test registry command boundaries and execute the real deploy script against
   the real Helm renderer. Document one-way mirroring and variable setup on one page.
5. Build/push to loopback registry:2, install by pull into an isolated kind cluster,
   record source revision and digests, verify endpoints and clean local resources.

## Red-team checklist

- No credentials in arguments, Helm values, image refs or committed fixtures.
- Failed gates cannot publish; deployment is protected, opt-in and manual.
- Deployment uses SHA refs and clears stale digest overrides; moving tags are not acceptance evidence.
- Immutable tag replacement is rejected by registry policy; failed pushes propagate.
- Existing source CI publishing and exact-SHA pull verification retain their behavior.

## Acceptance

Named registry/CI/Helm regression, scoped vet, just check and affected package
tests; local registry-to-kind smoke with synthetic data. No claims of remote
GitLab execution, real channel delivery, runtime Pod support or enforcing-CNI tests.
