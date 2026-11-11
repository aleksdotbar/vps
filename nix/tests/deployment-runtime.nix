{ pkgs, panelBinary, testBinary }:
let
  panel = pkgs.runCommand "panel-native-fixture" { } ''
    mkdir -p "$out/bin"
    install -m 0755 ${panelBinary} "$out/bin/panel"
  '';
  image = pkgs.dockerTools.buildImage {
    name = "panel-deployment-fixture";
    tag = "test";
    copyToRoot = pkgs.buildEnv {
      name = "panel-deployment-fixture-root";
      paths = [ pkgs.busybox ];
      pathsToLink = [ "/bin" ];
    };
    extraCommands = ''
      mkdir -p www tmp
      chmod 1777 tmp
      echo panel-fixture-ok > www/index.html
    '';
    config.User = "1000:1000";
  };
in
pkgs.testers.runNixOSTest {
  name = "panel-native-deployment-recovery";
  requiredFeatures.kvm = false;
  qemu.forceAccel = false;
  globalTimeout = 1800;

  nodes.server = { config, lib, ... }: {
    imports = [ ../modules/base.nix ];
    system.stateVersion = "26.05";
    services.tailscale.enable = lib.mkForce false;
    services.openssh.settings.AllowUsers = [ "operator" ];
    users.users.operator = {
      isNormalUser = true;
      extraGroups = [ "wheel" ];
      openssh.authorizedKeys.keys = [
        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea public-build-fixture-only"
      ];
    };
    virtualisation = {
      memorySize = 3072;
      cores = 2;
      diskSize = 4096;
      restrictNetwork = true;
      additionalPaths = [ config.system.build.toplevel ];
    };
    nix.enable = true;
    environment.systemPackages = [ panel pkgs.curl pkgs.python3 pkgs.docker-compose ];
    # Only this isolated VM maps GHCR to a local, maintained registry.
    networking.hosts."127.0.0.1" = [ "ghcr.io" ];
    virtualisation.docker.daemon.settings."insecure-registries" = [ "ghcr.io" ];
    services.dockerRegistry = { enable = true; port = 80; };
    environment.etc."panel-fixture-image".source = image;
    environment.etc."panel-fixture-test".source = testBinary;
    environment.etc."panel-host.json".text = builtins.toJSON {
      schema_version = 1;
      installation_id = "11111111-1111-4111-8111-111111111111";
      server_id = "22222222-2222-4222-8222-222222222222";
      role = "panel";
      stage = "final";
    };
  };
  testScript = builtins.readFile ./deployment-runtime.py;
}
