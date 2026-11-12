# Stateless project example

- Compose definitions: [shared](compose.yaml), [staging](compose.staging.yaml), [production](compose.production.yaml).
- Build workflow: [.github/workflows/build.yaml](.github/workflows/build.yaml).

## Adapt for an application

1. Copy the Compose files and workflow into the application repository.
2. Supply real Dockerfiles and image healthchecks; retain graceful SIGTERM handling.
3. Write every service image as `ghcr.io/<owner>/<name>:${GIT_SHA:?set by the panel}`. The panel sets `GIT_SHA` to the full commit SHA it deploys.
4. Make the GHCR packages public. Private images are outside MVP.
5. Configure environment secrets and domains through the panel. Keep credentials, host ports and host-specific network names out of the repository.

## Build contract

- Any CI may build; it only has to push each image tagged with the full commit SHA for linux/amd64.
- The panel waits for every service's tag, resolves it to an immutable digest and deploys that digest.
- Keep runtime secrets, SSH keys and provider credentials out of build jobs.

## Configuration checks

Use synthetic values and native Compose validation, e.g. `GIT_SHA=<sha> docker compose -f compose.yaml -f compose.production.yaml config --quiet`. Never print resolved secret-bearing configuration.
