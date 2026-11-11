{ nixpkgs, instance }:
let
  inherit (nixpkgs) lib;
  firewall = instance.config.networking.firewall;
  transport = instance.config.services.tailscale.interfaceName;
  publicSsh = instance.extendModules {
    modules = [ { networking.firewall.interfaces.public.allowedTCPPorts = [ 22 ]; } ];
  };
  refusesPublicPorts = lib.all (port:
    !(builtins.tryEval (instance.extendModules {
      modules = [ { networking.firewall.allowedTCPPorts = [ port ]; } ];
    }).config.system.build.toplevel.drvPath).success
  ) [ 22 80 443 ];
in
assert lib.assertMsg
  (builtins.elem "lo" firewall.trustedInterfaces && !(builtins.elem transport firewall.trustedInterfaces))
  "Loopback must stay usable without trusting every Tailscale service.";
assert lib.assertMsg (!(builtins.tryEval publicSsh.config.system.build.toplevel.drvPath).success)
  "The initial policy must reject an accidental public-interface SSH opening.";
assert lib.assertMsg refusesPublicPorts
  "Public SSH, HTTP and HTTPS openings must fail admission; web ingress uses an outbound tunnel.";
nixpkgs.legacyPackages.x86_64-linux.runCommand "foundation-network-admission" { } ''
  touch "$out"
''
