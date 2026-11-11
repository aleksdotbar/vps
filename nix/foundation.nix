# Only canonical input written by `panel install build` belongs here. Go owns
# validation; this is a fixed native projection, not a Nix importer.
{ nixpkgs, inputJSON, sourceSHA }:
let
  inherit (nixpkgs) lib;
  inputBytes = builtins.readFile inputJSON;
  input = builtins.fromJSON inputBytes;
  inputSHA = builtins.hashString "sha256" inputBytes;
  isPanel = input.role == "panel";
  operator = input.operator;
  bootstrap = input.bootstrap;
  pkgs = nixpkgs.legacyPackages.x86_64-linux;
  operatorSudoRule = {
    users = [ operator.name ];
    groups = [ ];
    host = "ALL";
    runAs = "root:root";
    commands = [ {
      command = "ALL";
      options = [ "NOPASSWD" "NOSETENV" ];
    } ];
  };
  panelPackage = import ./package-panel.nix {
    inherit pkgs sourceSHA;
  };
  common = [
    (import ./modules/machine.nix input.machine)
    ./modules/executor.nix
    ./modules/tunnel.nix
    ({ config, ... }: {
      services.vpsExecutor.enable = true;
      services.vpsTunnel.enable = true;
      users.mutableUsers = true;
      users.users.${operator.name} = {
        isNormalUser = true;
        extraGroups = [ "wheel" ];
        openssh.authorizedKeys.keys = [ operator.public_key ];
      };
      # The reviewed operator key is independent administrative authority.
      # Keep other wheel users password-gated and the executor helper-restricted.
      security.sudo.extraRules = [ operatorSudoRule ];
      assertions = [ {
        assertion = config.security.sudo.wheelNeedsPassword
          && lib.all (rule:
            !(builtins.elem "panel" rule.users)
            && !(builtins.elem "panel" rule.groups)
            && !(builtins.elem "ALL" rule.groups)
            && (!(builtins.elem "wheel" rule.groups)
              || lib.all (command: builtins.isString command
                || !(builtins.elem "NOPASSWD" command.options)
              ) rule.commands)
          ) config.security.sudo.extraRules
          && builtins.filter (rule:
            builtins.elem operator.name rule.users
            || builtins.elem "ALL" rule.users
          ) config.security.sudo.extraRules == [ operatorSudoRule ];
        message = "The reviewed operator requires its exact passwordless root rule; other wheel identities must remain password-gated.";
      } ];
      environment.systemPackages = [ panelPackage ];
      # The panel module supplies the same path for its role. Native home and
      # declared human-key paths remain in OpenSSH's merged default list.
      services.openssh.authorizedKeysFiles =
        lib.optionals (!isPanel) [ "/var/lib/panel-access/%u/authorized_keys" ];
    })
  ] ++ lib.optionals isPanel [
    ./modules/panel.nix
    { services.vpsPanel = { enable = true; package = panelPackage; }; }
  ];
  identity = stage: {
    schema_version = 1;
    installation_id = input.installation_id;
    server_id = input.server_id;
    role = input.role;
    inherit stage;
    source_sha = sourceSHA;
    input_sha256 = inputSHA;
    # Go hashes its exact canonical machine bytes. Nix toJSON orders keys
    # differently and must not be used to recompute this hash.
    machine_sha256 = input.machine_sha256;
  };
  system = stage: modules: lib.nixosSystem {
    modules = common ++ modules ++ [ ({ config, ... }:
      let public = import ./installer/public-accounts.nix { inherit lib config; };
      in {
        environment.etc."panel-host.json".text = builtins.toJSON ((identity stage) // {
          mutable_users = config.users.mutableUsers;
          accounts = builtins.attrValues (lib.filterAttrs
            (name: _: config.users.users.${name}.enable) public.accounts);
          groups = builtins.attrValues public.groups;
        });
      }) ];
  };
in
{
  inherit panelPackage;
  enrollment = system "enrollment" [
    ./modules/enrollment.nix
    {
      networking.firewall.allowedTCPPorts = lib.optional bootstrap.public_ssh22 22;
      services.openssh = {
        enable = true;
        openFirewall = false;
        settings = {
          AuthenticationMethods = "any";
          PermitRootLogin = bootstrap.root_login;
          PasswordAuthentication = bootstrap.password_authentication;
          KbdInteractiveAuthentication = bootstrap.keyboard_interactive_authentication;
          AllowUsers = [ operator.name ] ++ lib.optional (bootstrap.root_login != "no") "root";
          AllowAgentForwarding = false;
          X11Forwarding = false;
        };
      };
    }
  ];
  final = system "final" ([
    ./modules/base.nix
    { services.openssh.settings.AllowUsers = [ operator.name ]; }
  ] ++ lib.optional isPanel { services.vpsPanel.startAtBoot = true; });
}
