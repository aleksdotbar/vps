# The panel host role, imported next to the provider's own configuration.nix.
# It adds services and firewall rules only; boot, disks and the provider's
# network configuration stay exactly as the provider ships them.
{ rev }:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.vpsPanel;
in
{
  imports = [
    ./panel.nix
    ./executor.nix
    ./tunnel.nix
    ./runtime.nix
    ./network.nix
    ./transport.nix
  ];

  options.services.vpsPanel.publicSSH = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = "Keep SSH reachable on public interfaces. Set false after confirming access over Tailscale.";
  };

  config = {
    services.vpsPanel = {
      enable = true;
      package = lib.mkDefault (import ../panel.nix { inherit pkgs rev; });
    };
    services.vpsExecutor.enable = true;
    services.vpsTunnel.enable = true;

    services.openssh.enable = true;
    services.openssh.openFirewall = cfg.publicSSH;

    # The executor's sudo rule runs the helper from the current system path.
    environment.systemPackages = [ cfg.package ];

    # Updates use `nix flake update` and `nixos-rebuild switch --flake /etc/nixos#panel`.
    nix.settings.experimental-features = [ "nix-command" "flakes" ];
  };
}
