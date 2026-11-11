{ config, lib, pkgs, ... }:
let
  docker = config.virtualisation.docker;
in
{
  virtualisation.docker = {
    enable = true;
    package = pkgs.docker;
    autoPrune.enable = false;
    listenOptions = [ "/run/docker.sock" ];
    logDriver = "local";
    daemon.settings = {
      "firewall-backend" = "iptables";
      iptables = true;
      ip6tables = true;
      "allow-direct-routing" = false;
      "live-restore" = true;
      "no-new-privileges" = true;
      bridge = "none";
      ip = "127.0.0.1";
      "default-network-opts".bridge."com.docker.network.bridge.host_binding_ipv4" = "127.0.0.1";
      "log-opts" = {
        "max-size" = "10m";
        "max-file" = "3";
      };
    };
  };

  environment.systemPackages = [ pkgs.cloudflared ];

  # Both activation paths require the policy before exposing the local API.
  systemd.services.docker = {
    requires = [ "nftables.service" ];
    after = [ "nftables.service" ];
  };
  systemd.sockets.docker = {
    requires = [ "nftables.service" ];
    after = [ "nftables.service" ];
  };

  assertions = [
    {
      assertion = docker.listenOptions == [ "/run/docker.sock" ]
        && docker.daemon.settings.hosts == [ "fd://" ]
        && docker.extraOptions == "";
      message = "Foundation Docker must use only its local Unix socket without extra daemon flags.";
    }
    {
      assertion = !docker.autoPrune.enable && !docker.autoPrune.allVolumes.enable;
      message = "Foundation must retain Docker release and recovery material; automatic pruning is forbidden.";
    }
    {
      assertion = docker.daemon.settings."firewall-backend" == "iptables"
        && docker.daemon.settings.iptables
        && docker.daemon.settings.ip6tables
        && !docker.daemon.settings."allow-direct-routing"
        && docker.daemon.settings.bridge == "none"
        && docker.daemon.settings.ip == "127.0.0.1"
        && docker.daemon.settings."default-network-opts".bridge."com.docker.network.bridge.host_binding_ipv4" == "127.0.0.1"
        && docker.daemon.settings."no-new-privileges"
        && docker.daemon.settings."log-driver" == "local"
        && docker.daemon.settings."log-opts" == { "max-size" = "10m"; "max-file" = "3"; };
      message = "Foundation Docker requires managed bridges, loopback bindings, native firewall isolation and bounded local logs.";
    }
  ];
}
