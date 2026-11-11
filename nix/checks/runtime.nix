{ nixpkgs, instance }:
let
  inherit (nixpkgs) lib;
  refuses = module:
    !(builtins.tryEval (instance.extendModules {
      modules = [ module ];
    }).config.system.build.toplevel.drvPath).success;
  pkgs = nixpkgs.legacyPackages.x86_64-linux;
  daemonConfig = (pkgs.formats.json { }).generate "daemon.json"
    instance.config.virtualisation.docker.daemon.settings;
  nftablesRules = builtins.elemAt instance.config.systemd.services.nftables.serviceConfig.ExecStart 1;
in
assert lib.assertMsg (lib.all refuses [
  { virtualisation.docker.listenOptions = lib.mkForce [ "0.0.0.0:2375" ]; }
  { virtualisation.docker.daemon.settings.hosts = lib.mkForce [ "tcp://[::]:2375" ]; }
  { virtualisation.docker.extraOptions = "--host=tcp://0.0.0.0:2375"; }
  { virtualisation.docker.autoPrune.enable = true; }
  { networking.nftables.flushRuleset = true; }
  { networking.firewall.extraForwardRules = lib.mkForce "accept"; }
  { networking.firewall.filterForward = false; }
]) "Unsafe Docker API, pruning or forwarding changes must fail foundation admission.";
pkgs.runCommand "foundation-runtime-admission" { } ''
  ${instance.config.virtualisation.docker.package}/bin/dockerd --validate --config-file=${daemonConfig}
  mkdir "$out"
  ln -s ${daemonConfig} "$out/daemon.json"
  ln -s ${nftablesRules} "$out/nftables-rules"
''
