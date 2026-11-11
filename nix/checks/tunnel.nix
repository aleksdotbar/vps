{ nixpkgs }:
let
  inherit (nixpkgs) lib;
  pkgs = nixpkgs.legacyPackages.x86_64-linux;
  instance = lib.nixosSystem {
    modules = [
      ../modules/tunnel.nix
      ./fixture.nix
      { services.vpsTunnel.enable = true; }
    ];
  };
  admitted = system: lib.all (entry: entry.assertion) system.config.assertions;
  refuses = module: !admitted (instance.extendModules { modules = [ module ]; });
in
assert lib.assertMsg (admitted instance)
  "The runtime-only Tunnel service must pass native NixOS admission without any installation identity or credentials.";
assert lib.assertMsg (lib.all refuses [
  { users.users.collision = { name = "panel-cloudflared"; isSystemUser = true; group = "root"; }; }
  { users.groups.collision.name = "panel-cloudflared"; }
  { users.groups.root.members = [ "panel-cloudflared" ]; }
  { users.users.root.group = lib.mkForce "panel-cloudflared"; }
  { users.users.root.extraGroups = [ "panel-cloudflared" ]; }
  { services.cloudflared.enable = true; }
  { systemd.services.panel-cloudflared.serviceConfig.User = "root"; }
  { systemd.services.panel-cloudflared.serviceConfig.Group = "docker"; }
  { systemd.services.panel-cloudflared.serviceConfig.SupplementaryGroups = [ "docker" ]; }
  { systemd.services.panel-cloudflared.serviceConfig.DynamicUser = lib.mkForce false; }
  { systemd.services.panel-cloudflared.serviceConfig.NoNewPrivileges = lib.mkForce false; }
  { systemd.services.panel-cloudflared.serviceConfig.CapabilityBoundingSet = lib.mkForce "CAP_SYS_ADMIN"; }
  { systemd.services.panel-cloudflared.serviceConfig.LoadCredential = lib.mkForce [ "credentials.json:/nix/store/credential" ]; }
  { systemd.services.panel-cloudflared.serviceConfig.ExecStart = lib.mkForce "cloudflared tunnel --metrics 0.0.0.0:20241 run"; }
]) "Static dynamic-identity collisions, competing Tunnel services, elevated identities/capabilities, store credentials and unsafe command overrides must fail admission.";
pkgs.writeText "tunnel-role-admission.json" (builtins.toJSON {
  unit = instance.config.systemd.units."panel-cloudflared.service".text;
})
