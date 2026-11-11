{ config, lib, pkgs, ... }:
let
  cfg = config.services.vpsPanel;
  transport = config.services.tailscale.interfaceName;
  firewall = config.networking.firewall;
  firewallUnit = if firewall.backend == "nftables" then "nftables.service" else "firewall.service";
  panelUser = config.users.users.panel;
  unit = config.systemd.services.panel.serviceConfig;
  includesHttps = ranges: lib.any (range: range.from <= 443 && range.to >= 443) ranges;
  publicHttpsClosed = !(builtins.elem 443 firewall.allowedTCPPorts)
    && !includesHttps firewall.allowedTCPPortRanges
    && lib.all (name:
      let ports = firewall.interfaces.${name}; in
      name == transport || (!(builtins.elem 443 ports.allowedTCPPorts) && !includesHttps ports.allowedTCPPortRanges)
    ) (builtins.attrNames firewall.interfaces);
in
{
  options.services.vpsPanel = {
    enable = lib.mkEnableOption "the private panel host role";
    package = lib.mkOption {
      type = lib.types.package;
      description = "Panel package containing bin/panel with the private HTTPS CLI contract.";
    };
    startAtBoot = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Enable the unit at boot after enrollment has proved private HTTPS and SSH access.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.groups.panel = { };
    users.users.panel = {
      isSystemUser = true;
      group = "panel";
      home = "/var/lib/panel";
    };

    services.tailscale.permitCertUid = "panel";
    # Runtime public keys are installed after credentials exist. keyFiles would
    # read their contents during CI evaluation/build and cannot serve this role.
    services.openssh.authorizedKeysFiles = [ "/var/lib/panel-access/%u/authorized_keys" ];

    systemd.services.panel = {
      description = "Private hosting panel";
      wantedBy = lib.optional cfg.startAtBoot "multi-user.target";
      requires = [ firewallUnit "tailscaled.service" ];
      after = [ firewallUnit "tailscaled.service" ];
      path = [ pkgs.openssh ];
      environment.GOMEMLIMIT = "512MiB";
      serviceConfig = {
        Type = "exec";
        ExecStart = lib.escapeShellArgs [
          "${cfg.package}/bin/panel" "serve" "-tailscale-https"
          "-state-dir" "/var/lib/panel"
          "-master-key-file" "/var/lib/panel-key/master.key"
          "-origin" "\${PANEL_ORIGIN}"
          "-compose-command" "${pkgs.docker-compose}/bin/docker-compose"
        ];
        User = "panel";
        Group = "panel";
        StateDirectory = [ "panel" "panel-key" ];
        StateDirectoryMode = "0700";
        RuntimeDirectory = "panel";
        RuntimeDirectoryMode = "0700";
        UMask = "0077";
        WorkingDirectory = "/var/lib/panel";
        EnvironmentFile = "/var/lib/panel-config/serve.env";
        CapabilityBoundingSet = [ "CAP_NET_BIND_SERVICE" ];
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        InaccessiblePaths = [ "-/run/docker.sock" ];
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        Restart = "on-failure";
        RestartSec = "5s";
        TimeoutStopSec = "45s";
        MemoryMax = "1G";
        TasksMax = 256;
        LimitNOFILE = 4096;
      };
    };

    assertions = [
      {
        assertion = config.services.tailscale.enable
          && transport != "userspace-networking"
          && config.services.tailscale.permitCertUid == "panel"
          && lib.all (flag: !(lib.hasPrefix "--operator" flag) && !(lib.hasPrefix "--tun" flag))
            config.services.tailscale.extraDaemonFlags
          && lib.all (flag: !(lib.hasPrefix "--operator" flag)) config.services.tailscale.extraSetFlags;
        message = "Panel HTTPS requires native Tailscale networking and certificate-only access for the panel user.";
      }
      {
        assertion = firewall.enable
          && firewall.trustedInterfaces == [ "lo" ]
          && firewall.extraInputRules == ""
          && publicHttpsClosed
          && (firewall.interfaces.${transport}.allowedTCPPorts or [ ]) == [ 22 443 ]
          && (firewall.interfaces.${transport}.allowedTCPPortRanges or [ ]) == [ ];
        message = "The panel role requires an enabled firewall with private SSH/HTTPS only on the Tailscale interface; public HTTPS and broad interface trust are forbidden.";
      }
      {
        assertion = panelUser.name == "panel"
          && panelUser.enable
          && panelUser.isSystemUser
          && !panelUser.isNormalUser
          && panelUser.group == "panel"
          && panelUser.extraGroups == [ ]
          && panelUser.uid != 0
          && config.users.groups.panel.gid != 0
          && lib.all (name:
            name == "panel" || !(builtins.elem "panel" config.users.groups.${name}.members)
          ) (builtins.attrNames config.users.groups)
          && unit.User == "panel"
          && unit.Group == "panel"
          && unit.NoNewPrivileges
          && unit.CapabilityBoundingSet == [ "CAP_NET_BIND_SERVICE" ]
          && unit.AmbientCapabilities == [ "CAP_NET_BIND_SERVICE" ];
        message = "The panel must run as its unprivileged system user with only private HTTPS bind capability and no supplementary groups.";
      }
    ];
  };
}
