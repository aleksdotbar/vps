{ pkgs }:
let
  fixtureRoot = pkgs.runCommand "foundation-http-fixture-root" { } ''
    mkdir -p "$out/www"
    echo foundation-fixture-ok > "$out/www/index.html"
  '';

in
pkgs.testers.runNixOSTest {
  name = "foundation-initial-host-without-tailscale";
  requiredFeatures.kvm = false;
  qemu.forceAccel = false;
  globalTimeout = 1800;

  defaults = {
    system.stateVersion = "26.05";
    virtualisation.vlans = [ 1 2 ];
    virtualisation.restrictNetwork = true;
  };

  nodes.server = { config, lib, ... }: {
    imports = [ ../modules/base.nix ];
    # Only the test transport differs: eth2 is an isolated management link.
    services.tailscale.enable = lib.mkForce false;
    services.tailscale.interfaceName = "eth2";
    services.openssh.settings.AllowUsers = [ "operator" ];
    users.users.operator = {
      isNormalUser = true;
      extraGroups = [ "wheel" ];
      openssh.authorizedKeys.keys = [
        "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea public-build-fixture-only"
      ];
    };
    users.users.stranger.isNormalUser = true;
    security.sudo.extraRules = [{
      users = [ "operator" ];
      commands = [{ command = "ALL"; options = [ "NOPASSWD" ]; }];
    }];

    networking.interfaces.eth1 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.10.1"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:10::1"; prefixLength = 64; }];
    };
    networking.interfaces.eth2 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.20.1"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:20::1"; prefixLength = 64; }];
    };

    nix.enable = true;
    system.switch.enable = true;
    # This lab direct-boots QEMU; real bootloader/firmware checks need a target.
    boot.loader.grub.enable = false;
    # The disk-backed overlay and lower-store mount otherwise race to create /sysroot/nix.
    boot.initrd.systemd.services.rw-sysroot-nix-store.after = [
      "sysroot-nix-.ro\\x2dstore.mount"
    ];
    virtualisation = {
      memorySize = 2048;
      cores = 2;
      diskSize = 4096;
      writableStoreUseTmpfs = false;
      additionalPaths = [ config.system.build.toplevel ];
    };
    environment.systemPackages = [ pkgs.curl ];
    # A live test-only listener proves firewall denial is not a missing backend.
    systemd.services.fixture-http = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.busybox}/bin/httpd -f -p 8080 -h ${fixtureRoot}/www";
    };
    specialisation.deny-operator.configuration.services.openssh.settings.DenyUsers = [ "operator" ];
  };

  nodes.client = { lib, ... }: {
    virtualisation.memorySize = 768;
    environment.systemPackages = [ pkgs.openssh pkgs.curl pkgs.netcat-openbsd ];
    networking.interfaces.eth1 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.10.2"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:10::2"; prefixLength = 64; }];
    };
    networking.interfaces.eth2 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.20.2"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:20::2"; prefixLength = 64; }];
    };
    networking.firewall.allowedTCPPorts = [ 8081 ];
    systemd.services.fixture-http = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.busybox}/bin/httpd -f -p 8081 -h ${fixtureRoot}/www";
    };
  };

  testScript = builtins.readFile ./initial-host.py;
}
