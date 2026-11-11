# Stateless project example

- Compose definitions: [shared](compose.yaml), [staging](compose.staging.yaml), [production](compose.production.yaml).
- Actions template: [.github/workflows/panel-build.yaml](.github/workflows/panel-build.yaml).
- Release metadata: [example](release.json), [schema](../../panel/contracts/release-artifact.schema.json).

## Adapt for an application

1. Copy the Compose files and workflow into the application repository.
2. Supply real Dockerfiles and image healthchecks; retain graceful SIGTERM handling.
3. Give every resolved service a unique image variable and matching artifact entry. Replace all synthetic digests/source metadata through actual builds.
4. Keep the workflow on the default branch for manual dispatch; protect deployment branches.
5. Configure environment secrets and domains through the panel. Keep credentials, host ports and host-specific network names out of the repository.

## Build contract

- Build in GitHub Actions; publish GHCR images with the workflow's scoped `GITHUB_TOKEN`.
- Emit release metadata from actual build digests and the workflow's source identity.
- Include every resolved service, including prebuilt sidecars.
- Keep runtime secrets, SSH keys and provider credentials out of build jobs.
- Treat publishing tags as handles; deploy only verified immutable digests.
- The example defines a contract, not a deployable application.

## Configuration checks

Use synthetic values and native Compose validation. With real secrets, use `config --quiet`; never print resolved secret-bearing configuration.
