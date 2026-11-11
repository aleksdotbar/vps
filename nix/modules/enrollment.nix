{ config, lib, ... }:
{
  imports = [ ./transport.nix ];

  # Existing instance configuration owns its initial SSH/account settings.
  # This grants only private transport access, never a public bootstrap port.
  networking.firewall.interfaces.${config.services.tailscale.interfaceName}.allowedTCPPorts =
    [ 22 ] ++ lib.optional (config.services.vpsPanel.enable or false) 443;
}
