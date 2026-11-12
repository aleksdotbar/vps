{ pkgs }:
let
  fixtureImage = pkgs.dockerTools.buildImage {
    name = "foundation-network-fixture";
    tag = "test";
    copyToRoot = pkgs.buildEnv {
      name = "foundation-network-fixture-root";
      paths = [ pkgs.busybox ];
      pathsToLink = [ "/bin" ];
    };
    extraCommands = ''
      mkdir -p www tmp
      chmod 1777 tmp
      echo foundation-network-ok > www/index.html
    '';
    config = {
      User = "1000:1000";
      Cmd = [ "/bin/httpd" "-f" "-p" "8080" "-h" "/www" ];
    };
  };
  fixtureRoot = pkgs.writeTextDir "index.html" "foundation-egress-ok";
in
pkgs.testers.runNixOSTest {
  name = "foundation-docker-network";
  requiredFeatures.kvm = false;
  qemu.forceAccel = false;
  globalTimeout = 1800;

  defaults = {
    system.stateVersion = "26.05";
    virtualisation.vlans = [ 1 2 ];
    virtualisation.restrictNetwork = true;
  };

  nodes.server = { lib, ... }: {
    imports = [ ../modules/runtime.nix ../modules/network.nix ../modules/transport.nix ];
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
    networking.interfaces.eth1 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.10.1"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:10::1"; prefixLength = 64; }];
    };
    networking.interfaces.eth2 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.20.1"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:20::1"; prefixLength = 64; }];
    };
    virtualisation = {
      memorySize = 2048;
      cores = 2;
      diskSize = 4096;
    };
    environment.systemPackages = [ pkgs.curl ];
    systemd.services.fixture-host-http = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.busybox}/bin/httpd -f -p 8082 -h ${fixtureRoot}";
    };
    environment.etc."foundation-fixture-image".source = fixtureImage;
  };

  nodes.client = { lib, ... }: {
    virtualisation.memorySize = 512;
    environment.systemPackages = [ pkgs.curl pkgs.netcat-openbsd ];
    networking.interfaces.eth1 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.10.2"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:10::2"; prefixLength = 64; }];
    };
    networking.interfaces.eth2 = {
      ipv4.addresses = lib.mkForce [{ address = "192.168.20.2"; prefixLength = 24; }];
      ipv6.addresses = lib.mkForce [{ address = "fd00:20::2"; prefixLength = 64; }];
    };
    networking.firewall.allowedTCPPorts = [ 8081 ];
    systemd.services.fixture-egress-http = {
      wantedBy = [ "multi-user.target" ];
      serviceConfig.ExecStart = "${pkgs.busybox}/bin/httpd -f -p 8081 -h ${fixtureRoot}";
    };
  };

  testScript = builtins.readFile ./docker-network.py;
}
