# Operator procedures

## Installation

1. Prepare: an x86_64 NixOS host (Hostinger's NixOS image works as installed), provider console access, and a Tailscale account with MagicDNS and HTTPS certificates enabled.
2. As root on the host, run from an exact pushed commit:

   ```sh
   nix --extra-experimental-features 'nix-command flakes' run 'github:aleksdotbar/vps/COMMIT?dir=nix#install'
   ```

   It writes `/etc/nixos/flake.nix` around the existing `configuration.nix`, runs `nixos-rebuild switch` and starts `panel setup`. Rerunning is safe.
3. Follow `panel setup`: log the host into Tailscale with the printed URL and set the administrator password. Open the printed private HTTPS origin; keep the master-key recovery copy separately protected.
4. After `ssh root@<tailscale address>` works, close public SSH: add `services.vpsPanel.publicSSH = false;` to `/etc/nixos/panel-local.nix` (create it as `{ services.vpsPanel.publicSSH = false; }` if absent) and run `nixos-rebuild switch --flake /etc/nixos#panel`.
5. Update with `nix flake update --flake /etc/nixos` and `nixos-rebuild switch --flake /etc/nixos#panel`; roll back with `nixos-rebuild switch --rollback`.

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
