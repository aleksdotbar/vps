# MVP requirements

## Product

- One self-hosted installation, one administrator, one server and multiple projects.
- One repository for the panel, installer, reusable host foundation and generic examples.
- Existing x86_64 NixOS hosts, including provider images such as Hostinger's; the provider's boot, disk, network and console access stay as shipped.
- No OS installation, provider API or automated host maintenance.
- Panel and applications may share a host; register that host automatically and prevent its removal.
- The panel manages its own host through restricted private SSH; no resident custom agent.
- GitHub Actions builds application images; deployments never build application source on hosts.
- NixOS manages host configuration; application releases never rebuild or roll back the OS.
- Private administration over Tailscale; public application ingress through Cloudflare Tunnel.

## Projects and environments

- Project: one GitHub repository with named environments.
- Environment: one server, branch, Compose override, secret namespace and domain configuration.
- Support staging/development and intentional production releases through separate branch bindings.
- Show Compose services individually; deploy and roll back the whole environment.
- Snapshot source/run identity, image digests, Compose configuration, domains and secret versions in immutable releases.
- Settings and secret edits take effect on the next deployment; retain already admitted selections.
- Supersede unstarted work when its branch/server/override binding changes.
- Reject server reassignment after deployment or while started work remains unresolved.

## Repository and builds

- Use native Compose merge, interpolation and validation; no custom project-definition language.
- Keep hostnames and secret values in panel state; application repositories declare services, healthchecks and required secret names.
- Accept prebuilt images by immutable GHCR digest; require complete service/image mapping.
- Restrict privileged containers, host namespaces, socket/device access, arbitrary host mounts and public origin ports.
- Enforce non-root execution, bounded resources/logs, health checks and graceful shutdown.
- Give each service only its declared secrets; never interpolate the panel's ambient environment.
- Use an installation-owned GitHub App and selected repositories. Supply a conventional Actions template.
- First deployment reuses an exact verified build or dispatches one; later eligible pushes deploy automatically.
- Poll durably with bounded retries and provider backoff; reconcile uncertain dispatch before repeating it.
- Use public GHCR images for MVP; keep runtime credentials off build jobs.

## Installation and trust

- Install with one root command: add the panel NixOS module next to the host's own configuration from a pinned revision, `nixos-rebuild switch`, then run first-time setup.
- Rollback uses native NixOS generations; the provider console remains the independent recovery path.
- Public SSH stays open until the operator closes it explicitly after confirming private access.
- Keep the panel unprivileged; use a separate restricted management identity and fixed privileged helper.
- Human operator administration is separate from machine management access.
- Keep management, Docker API and application origins private over IPv4 and IPv6.

## Deployment and recovery

- Permit one unresolved deployment per environment; exclude conflicting host setup and shared ingress changes.
- Persist intent before effects; reconcile duplicate delivery, panel restart, lost SSH response and target reboot.
- Distinguish confirmed success, failure, recovery and unknown/partial outcome. Unknown work blocks conflicting mutations.
- Prepare images/configuration before replacement; verify actual service health and required routing before success.
- On confirmed replacement failure, restore the previous environment and verify recovery.
- On failed first deployment, remove only owned transient resources; never delete application data or volumes.
- Restore only the selected environment's route contribution; preserve other environments and unmanaged DNS records.
- Retain current/previous releases and unresolved-operation material; retire unreferenced terminal material under bounded retention.
- Accept brief replacement downtime. No data rollback, revoked-secret recovery or exactly-once remote-execution promise.

## Secrets and administrator access

- One password-authenticated administrator; no signup or team system.
- Use established password hashing, server-side sessions, login limits and mutation-origin protection.
- Local password reset invalidates existing sessions.
- Encrypt secrets with standard authenticated encryption; keep the master key separate with an independent recovery copy.
- Never place plaintext credentials, private keys or password hashes in Git, Nix inputs/store, logs, arguments or job payloads.
- Return secret names/version metadata, never saved plaintext. Preserve omitted keys and explicitly empty values during edits.

## Interface

- Servers, projects, environment tabs, service observations, deployment history and whole-environment rollback.
- Usable from desktop and phone; explicit errors, empty states and unresolved outcomes.
- Unary generated RPCs; mutations return operation identity promptly; status uses polling.
- Route loaders await required data through the shared query cache; validate URL state and keep secrets out of URLs.
- Retain the current page during navigation and show a top progress indicator; background polling must not trigger it.
- Use compiler-managed memoization; optimistic updates must not claim deployment or secret-write success.
- Embed production assets in the server binary; serve API and UI from one origin.

## Acceptance

- Install the panel on its deployment host, connect a repository, configure two environments and deploy the external stateless webhook bot.
- Verify staging pushes leave production untouched and service-secret isolation works.
- Verify failed replacement recovery, interrupted-operation reconciliation and honest service observations.
- Verify private administration, public webhook authentication and origin isolation.
- Keep application-specific identities, credentials and configuration outside product source.

## Outside MVP

- Additional-server enrollment, guided installation, private images and panel backup/restore.

- Application databases/volumes and their backups, migrations/hooks, R2 automation.
- Logs/metrics UI, terminals, teams/RBAC, previews, rolling or zero-downtime deployment.
- Kubernetes/Swarm, buildpacks, provider management, control-plane HA and plugin frameworks.
