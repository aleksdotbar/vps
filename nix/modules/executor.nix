{ config, lib, pkgs, ... }:
let
  cfg = config.services.vpsExecutor;
  runner = config.users.users.panel-runner;
  helperRule = {
    users = [ "panel-runner" ];
    groups = [ ];
    host = "ALL";
    runAs = "root:root";
    commands = [ {
      command = "/run/current-system/sw/bin/panel host-helper";
      options = [ "NOPASSWD" "NOSETENV" ];
    } ];
  };
  runnerRules = builtins.filter (rule:
    builtins.elem "panel-runner" rule.users || builtins.elem "ALL" rule.users
    || builtins.elem "panel-runner" rule.groups
  ) config.security.sudo.extraRules;
in
{
  options.services.vpsExecutor.enable = lib.mkEnableOption "the restricted private host executor";

  config = lib.mkIf cfg.enable {
    users.groups.panel-runner = { };
    users.users.panel-runner = {
      isSystemUser = true;
      group = "panel-runner";
      home = "/var/empty";
      # OpenSSH invokes ForceCommand through the account shell with -c.
      shell = pkgs.bashInteractive;
    };

    # No global AllowUsers: the provider's own logins stay as configured. The
    # Match block alone confines panel-runner to Tailscale and the helper.
    services.openssh.settings.PermitUserEnvironment = false;
    services.openssh.extraConfig = lib.mkBefore ''
      Match User panel-runner
        AllowUsers panel-runner@100.64.0.0/10 panel-runner@fd7a:115c:a1e0::/48
        AuthorizedKeysFile /var/lib/panel-access/panel-runner/authorized_keys
        AuthorizedKeysCommand none
        TrustedUserCAKeys none
        AuthorizedPrincipalsFile none
        AuthorizedPrincipalsCommand none
        AuthenticationMethods publickey
        PubkeyAuthentication yes
        PasswordAuthentication no
        KbdInteractiveAuthentication no
        HostbasedAuthentication no
        ForceCommand /run/wrappers/bin/sudo -n -- /run/current-system/sw/bin/panel host-helper
        DisableForwarding yes
        AllowAgentForwarding no
        AllowTcpForwarding no
        AllowStreamLocalForwarding no
        X11Forwarding no
        PermitTunnel no
        PermitTTY no
        PermitUserRC no
        AcceptEnv LANG LC_*
      Match all
    '';

    security.sudo.extraRules = [ helperRule ];

    assertions = [
      {
        assertion = runner.enable && runner.name == "panel-runner"
          && runner.isSystemUser && !runner.isNormalUser
          && runner.uid != 0 && config.users.groups.panel-runner.gid != 0
          && runner.group == "panel-runner" && runner.extraGroups == [ ]
          && runner.home == "/var/empty" && !runner.createHome
          && runner.shell == pkgs.bashInteractive
          && runner.openssh.authorizedKeys.keys == [ ]
          && runner.openssh.authorizedKeys.keyFiles == [ ]
          && lib.all (name:
            !(builtins.elem "panel-runner" config.users.groups.${name}.members)
          ) (builtins.attrNames config.users.groups);
        message = "The private executor must remain its unprivileged system identity without supplementary groups or store-declared SSH keys.";
      }
      {
        assertion = config.security.sudo.enable && !config.security.sudo.execWheelOnly
          && runnerRules == [ helperRule ]
          && config.services.openssh.settings.PermitUserEnvironment == false
          && config.services.openssh.package == pkgs.openssh;
        message = "The private executor requires exactly the fixed passwordless helper rule and no accepted user environment.";
      }
    ];
  };
}
