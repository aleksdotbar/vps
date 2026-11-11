{ config, lib, ... }:
let
  firewall = config.networking.firewall;
  transport = config.services.tailscale.interfaceName;
  privatePorts = [ 22 ] ++ lib.optional (config.services.vpsPanel.enable or false) 443;
  onlyDhcp = ports: lib.all (port: builtins.elem port [ 68 546 ]) ports;
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
    trustedInterfaces = lib.mkForce [ "lo" ];
    allowedTCPPorts = lib.mkDefault [ ];
    allowedTCPPortRanges = lib.mkForce [ ];
    allowedUDPPortRanges = lib.mkForce [ ];
    interfaces.${transport}.allowedTCPPorts = privatePorts;
  };

  # Stop the listening endpoint before removing the policy that makes SSH private.
  systemd.services.sshd = lib.mkIf (!config.services.openssh.startWhenNeeded) {
    requires = [ "nftables.service" ];
    after = [ "nftables.service" ];
  };
  systemd.sockets.sshd = lib.mkIf config.services.openssh.startWhenNeeded {
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
        && firewall.allowedTCPPorts == [ ]
        && onlyDhcp firewall.allowedUDPPorts
        && firewall.extraInputRules == ""
        && lib.all (name:
          let ports = firewall.interfaces.${name}; in
          ports.allowedTCPPorts == (if name == transport then privatePorts else [ ])
          && ports.allowedTCPPortRanges == [ ]
          && onlyDhcp ports.allowedUDPPorts
          && ports.allowedUDPPortRanges == [ ]
        ) (builtins.attrNames firewall.interfaces);
      message = "Foundation permits only private SSH, optional panel HTTPS and DHCP; public application and management ports must stay closed.";
    }
  ];
}
