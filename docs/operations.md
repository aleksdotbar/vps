# Operator procedures

## Installation

1. Prepare: a running x86_64 NixOS host (Hostinger's NixOS image works as installed), provider console access, a Tailscale account with MagicDNS and HTTPS certificates enabled, and your Ed25519 public key.
2. As root on the host, run from an exact pushed commit:

   ```sh
   nix --extra-experimental-features 'nix-command flakes' run 'github:aleksdotbar/vps/COMMIT?dir=nix#install' -- --operator NAME --operator-key 'ssh-ed25519 ...'
   ```

   Review the discovered host configuration; the host then builds and switches to the enrollment configuration. Public SSH stays open.
3. Open a new SSH session to the host and run `sudo panel install` before the printed deadline, or the host restores its previous configuration. Log the host into Tailscale with the printed URL and set the administrator password.
4. From your workstation, run `ssh -t NAME@TAILSCALE_IP sudo panel install` and confirm provider console access. This closes public SSH.
5. Open another new session the same way before the printed deadline to confirm the final configuration.
6. Reboot, then run `sudo panel install` again to verify the booted configuration. Open the printed private HTTPS origin and keep the master-key recovery copy separately protected.

Every run of `sudo panel install` resumes from recorded state and never repeats an uncertain step. Before enrollment starts, `sudo panel install --reset` discards the recorded build.

## Project setup

1. Connect the installation-owned GitHub App and grant selected-repository access.
2. Commit the [application workflow and Compose example](../examples/stateless/README.md) to the application repository, adapting its real image builds and healthchecks.
3. Create the project and bind each environment to its server, branch and Compose override.
4. Enter environment secrets and hostname/service/port mappings in the panel.
5. Publish application images publicly in GHCR. Private images are outside MVP.
6. Connect Cloudflare using an account-owned token with **Cloudflare One Connector: cloudflared Write** and **DNS Write** restricted to the selected zones; provision the target Tunnel.
7. Submit deployment, follow its operation and verify public application behavior.

## Changes and recovery

- Saving secrets or domains stages configuration for the next deployment.
- Resolve unknown operations before starting conflicting work; a connection failure does not prove a remote failure.
- Whole-environment rollback restores retained application configuration, images, routes and secret references. It does not restore data or external side effects.
- Keep the panel infrastructure outside application projects.

## Administrator password reset

Stop the service; run as its state-owning account with the configured state/key paths:

```sh
panel reset-password -state-dir /absolute/private/state -master-key-file /absolute/private/master-key
```

Restart the service after reset. Supply passwords through protected terminal/stdin, never arguments or environment variables.

## Logs

Run through human administrator SSH on the panel host:

```sh
sudo journalctl --unit='panel-deploy-*' --since='1 hour ago' --no-pager
sudo docker logs --tail=100 CONTAINER_ID
```

Find `CONTAINER_ID` with `sudo docker ps`. Add `--follow` to stream application logs.
