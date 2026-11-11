{ nixpkgs, panelPackage }:
let
  inherit (nixpkgs) lib;
  pkgs = nixpkgs.legacyPackages.x86_64-linux;
  common = [
    ../modules/panel.nix
    ./fixture.nix
    { services.vpsPanel = { enable = true; package = panelPackage; }; }
  ];
  enrollment = lib.nixosSystem {
    modules = common ++ [
      ../modules/enrollment.nix
      {
        services.openssh = { enable = true; openFirewall = true; };
      }
    ];
  };
  final = lib.nixosSystem {
    modules = common ++ [
      ../modules/base.nix
      { services.vpsPanel.startAtBoot = true; }
    ];
  };
  canEvaluate = instance:
    (builtins.tryEval instance.config.system.build.toplevel.drvPath).success;
  refuses = instance: module:
    !canEvaluate (instance.extendModules { modules = [ module ]; });
  transport = final.config.services.tailscale.interfaceName;
in
assert lib.assertMsg (canEvaluate enrollment && canEvaluate final)
  "Both panel enrollment and final roles must pass native NixOS admission with the supplied panel package.";
assert lib.assertMsg
  (enrollment.config.systemd.services.panel.wantedBy == [ ]
    && enrollment.config.systemd.services.panel.enable
    && final.config.systemd.services.panel.wantedBy == [ "multi-user.target" ])
  "Enrollment must provide a manually startable unit without boot startup; only the final role enables boot startup.";
assert lib.assertMsg
  (enrollment.config.networking.firewall.interfaces.${transport}.allowedTCPPorts == [ 22 443 ]
    && final.config.networking.firewall.interfaces.${transport}.allowedTCPPorts == [ 22 443 ]
    && builtins.elem "firewall.service" enrollment.config.systemd.services.panel.requires
    && builtins.elem "nftables.service" final.config.systemd.services.panel.requires)
  "Private HTTPS must work with the enrollment and final native firewall backends without broad interface trust.";
assert lib.assertMsg (lib.all (refuses enrollment) [
  { networking.firewall.enable = false; }
  { networking.firewall.allowedTCPPorts = [ 443 ]; }
  { networking.firewall.allowedTCPPortRanges = [ { from = 440; to = 444; } ]; }
  { networking.firewall.interfaces.public.allowedTCPPorts = [ 443 ]; }
  { networking.firewall.interfaces.${transport}.allowedTCPPortRanges = [ { from = 444; to = 445; } ]; }
  { networking.firewall.trustedInterfaces = [ "lo" transport ]; }
  { services.tailscale.interfaceName = "userspace-networking"; }
  { users.users.panel.extraGroups = [ "docker" ]; }
  { users.groups.docker.members = [ "panel" ]; }
  { systemd.services.panel.serviceConfig.User = lib.mkForce "root"; }
]) "Disabled firewall, public HTTPS, interface trust, userspace Tailscale and elevated panel identity must fail admission.";
assert lib.assertMsg (refuses final {
  networking.firewall.interfaces.${transport}.allowedTCPPorts = [ 80 ];
}) "The panel role must not broaden the foundation's private TCP set beyond SSH and HTTPS.";
pkgs.writeText "panel-role-admission.json" (builtins.toJSON {
  enrollmentUnit = enrollment.config.systemd.units."panel.service".text;
  finalUnit = final.config.systemd.units."panel.service".text;
})
