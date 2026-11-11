{ config, lib, pkgs, ... }:
let
  cfg = config.services.vpsTunnel;
  identity = "panel-cloudflared";
  directory = "/var/lib/panel-cloudflared";
  configuration = "${directory}/config.json";
  credentials = "${directory}/credentials.json";
  cloudflared = "${pkgs.cloudflared}/bin/cloudflared";
  command = lib.escapeShellArgs [
    cloudflared "tunnel" "--config" configuration "--no-autoupdate"
    "--metrics" "127.0.0.1:20241" "--loglevel" "warn"
    "--management-diagnostics=false" "--grace-period" "20s" "--retries" "5"
    "run" "--credentials-file" "%d/credentials.json"
  ];
  unit = config.systemd.services.panel-cloudflared.serviceConfig;
in
{
  options.services.vpsTunnel.enable = lib.mkEnableOption "the panel-managed local Cloudflare Tunnel connector";

  config = lib.mkIf cfg.enable {
    # Routes and the tunnel UUID are nonsecret runtime data. Only root publishes
    # config.json (0644) and credentials.json (0600); neither is a Nix input.
    systemd.tmpfiles.rules = [ "d ${directory} 0755 root root -" ];

    # The upstream module generates immutable tunnel identity and ingress in the
    # store. This unit instead consumes the panel's mutable native configuration.
    systemd.services.panel-cloudflared = {
      description = "Panel-managed Cloudflare Tunnel connector";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      unitConfig = {
        ConditionPathExists = [ configuration credentials ];
        # Paced automatic restarts stay below this limit; repeated external
        # starts remain bounded without stranding ordinary outage recovery.
        StartLimitIntervalSec = 300;
        StartLimitBurst = 10;
      };
      environment.GOMEMLIMIT = "384MiB";
      serviceConfig = {
        # Upstream notifies systemd after its first edge connection. This does
        # not prove ongoing connector health, route readiness or public HTTPS.
        Type = "notify";
        NotifyAccess = "main";
        ExecStartPre = lib.escapeShellArgs [ cloudflared "tunnel" "--config" configuration "ingress" "validate" ];
        ExecStart = command;
        # The CLI override preserves the compiler's fixed source credential path
        # while granting this dynamic user only the protected systemd snapshot.
        LoadCredential = [ "credentials.json:${credentials}" ];
        DynamicUser = true;
        UMask = "0077";
        NoNewPrivileges = true;
        CapabilityBoundingSet = "";
        AmbientCapabilities = "";
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        ProtectClock = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
        InaccessiblePaths = [ "-/run/docker.sock" ];
        Restart = "on-failure";
        RestartSec = "60s";
        TimeoutStartSec = "90s";
        TimeoutStopSec = "30s";
        MemoryMax = "512M";
        CPUQuota = "100%";
        TasksMax = 128;
        LimitNOFILE = 4096;
        LogRateLimitIntervalSec = "30s";
        LogRateLimitBurst = 100;
      };
    };

    assertions = [
      {
        assertion = lib.all (user:
          user.name != identity
          && user.group != identity
          && !(builtins.elem identity user.extraGroups)
        ) (builtins.attrValues config.users.users)
          && lib.all (group:
            group.name != identity
            && !(builtins.elem identity group.members)
          ) (builtins.attrValues config.users.groups);
        message = "The panel-cloudflared dynamic identity must not collide with a static user, group or membership reference.";
      }
      {
        assertion = !config.services.cloudflared.enable;
        message = "The panel-managed connector must be the only configured cloudflared service; tunnel identity and routes remain runtime data.";
      }
      {
        assertion = unit.DynamicUser
          && !(unit ? User)
          && !(unit ? Group)
          && (unit.SupplementaryGroups or [ ]) == [ ]
          && unit.NoNewPrivileges
          && unit.CapabilityBoundingSet == ""
          && unit.AmbientCapabilities == ""
          && unit.LoadCredential == [ "credentials.json:${credentials}" ]
          && unit.ExecStart == command;
        message = "The panel-managed connector requires its unprivileged dynamic identity, runtime-only credential snapshot and fixed local configuration command.";
      }
    ];
  };
}
