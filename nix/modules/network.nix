{ config, lib, ... }:
let
  firewall = config.networking.firewall;
  transport = config.services.tailscale.interfaceName;
in
{
  networking.nftables.enable = true;
  networking.nftables.flushRuleset = false;
  # Drop before native firewall DNAT acceptance. Only executor-owned bridges
  # may forward; an accept here still passes through Docker and NixOS policy.
  networking.nftables.tables.foundation-forward = {
    family = "inet";
    content = ''
      chain guard {
        type filter hook forward priority filter - 10; policy drop;
        ct state invalid drop
        iifname "${transport}" drop
        oifname "${transport}" drop
        iifname "vps-*" accept
        oifname "vps-*" ct state { established, related } accept
      }
    '';
  };
  networking.firewall = {
    enable = true;
    backend = "nftables";
    filterForward = true;
    extraForwardRules = ''iifname "vps-*" accept'';
    # Private SSH and, on the panel host, its HTTPS origin. Public SSH follows
    # services.openssh.openFirewall, which the operator closes explicitly.
    interfaces.${transport}.allowedTCPPorts =
      [ 22 ] ++ lib.optional (config.services.vpsPanel.enable or false) 443;
  };

  # Stop the listening endpoint before removing the policy that makes SSH private.
  systemd.services.sshd = lib.mkIf (config.services.openssh.enable && !config.services.openssh.startWhenNeeded) {
    requires = [ "nftables.service" ];
    after = [ "nftables.service" ];
  };
  systemd.sockets.sshd = lib.mkIf (config.services.openssh.enable && config.services.openssh.startWhenNeeded) {
    requires = [ "nftables.service" ];
    after = [ "nftables.service" ];
  };

  assertions = [
    {
      assertion = firewall.enable
        && firewall.backend == "nftables"
        && firewall.filterForward
        && !config.networking.nftables.flushRuleset
        && firewall.extraForwardRules == ''iifname "vps-*" accept''
        && !(builtins.elem transport firewall.trustedInterfaces);
      message = "The host firewall must stay enabled with forwarding limited to panel-managed bridges and no blanket Tailscale trust.";
    }
  ];
}
