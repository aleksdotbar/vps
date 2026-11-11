{ config, lib, ... }:
let
  allowUsers = config.services.openssh.settings.AllowUsers;
  operators = if allowUsers == null then [ ] else allowUsers;
  executorEnabled = config.services.vpsExecutor.enable or false;
  humanOperators = builtins.filter (name: name != "panel-runner") operators;
  hasOperatorIdentity = name:
    let
      user = config.users.users.${name} or null;
    in
    name != "root"
    && user != null
    && user.enable
    && user.name == name
    && user.isNormalUser
    && builtins.elem "wheel" user.extraGroups
    && (user.openssh.authorizedKeys.keys != [ ] || user.openssh.authorizedKeys.keyFiles != [ ]);
in
{
  imports = [ ./transport.nix ];

  assertions = [
    {
      assertion = humanOperators != [ ] && lib.all hasOperatorIdentity humanOperators
        && (!(builtins.elem "panel-runner" operators) || executorEnabled);
      message = "Foundation SSH requires explicit normal wheel operators with declared public keys; only the guarded private executor may be an additional login.";
    }
  ];

  services.openssh = {
    enable = true;
    openFirewall = false;
    settings = {
      AuthenticationMethods = "publickey";
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "no";
      AllowAgentForwarding = false;
      X11Forwarding = false;
    };
  };
}
