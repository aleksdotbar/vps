# Run from the repository root with read-only/offline Nix evaluation, IFD disabled,
# and --max-jobs 0 --cores 1. Evaluate each named case separately, for example:
# --expr 'let f = builtins.getFlake ("path:" + toString ./nix); in
#   import ./nix/checks/legacy.nix { nixpkgs = f.inputs.nixpkgs.outPath; case = "supported"; }'
# The literal positive must round-trip; each negative must refuse with its fixed
# reason and omit machine input. No build, VM, target inspection or activation.
{ nixpkgs, case ? "supported" }:
let
  # Only shape reasons remain: the projection must yield a bootable machine and
  # no longer compares the host's other settings.
  expectedReasons = {
    supported = null;
    plain-declarative-key = null;
    restricted-declarative-key = null;
    custom-unit = null;
    qemu-agent = null;
    cloud-init = "network-declaration";
    auto-resize = "destructive-storage";
    hostinger = null;
  };
  hostingerCase = case == "hostinger";
  hostingerHost = ./hostinger-image.nix;
  # The image's addresses come from cloud-init; the operator reviews them.
  hostingerMachine = {
    schema_version = 1;
    system = "x86_64-linux";
    state_version = "26.05";
    template = "hostinger-kvm";
    boot = {
      loader = "grub-bios";
      grub_device = "/dev/sda";
      efi_can_touch_variables = false;
      efi_install_as_removable = false;
      grub_copy_kernels = false;
      configuration_limit = 100;
      initrd_systemd = false;
      initrd_available_kernel_modules = [ "virtio_pci" "virtio_blk" ];
      console = [ "tty0" "ttyS0,115200" ];
    };
    file_systems = [ {
      mount_point = "/";
      device = "/dev/disk/by-label/nixos";
      fs_type = "ext4";
      options = [ "defaults" ];
      needed_for_boot = true;
    } ];
    network = {
      backend = "networkd";
      host_name = "srv";
      use_dhcp = false;
      ipv6_mode = "static";
      nameservers = [ "192.0.2.53" ];
      interfaces = [ {
        name = "eth0";
        mtu = null;
        dhcp = null;
        ipv4_addresses = [ { address = "192.0.2.10"; prefix_length = 24; } ];
      } ];
      default_gateway = { address = "192.0.2.1"; interface = "eth0"; metric = null; };
      default_gateway6 = null;
    };
  };
  plainKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea generic-fixture";
  restrictedKey = ''restrict,command="/run/current-system/sw/bin/false" ${plainKey}'';
  evaluated = import (nixpkgs + "/nixos") {
    system = "x86_64-linux";
    configuration = if hostingerCase then hostingerHost else { lib, pkgs, ... }: lib.mkMerge [ {
      system.stateVersion = "26.05";
      boot.loader.grub.devices = [ "/dev/vda" ];
      boot.loader.grub.configurationLimit = 100;
      boot.initrd.availableKernelModules = [ "virtio_pci" "virtio_blk" ];
      boot.kernelParams = [ "console=tty0" "console=ttyS0,115200n8" ];
      boot.kernel.sysctl = {
        "net.ipv6.conf.ens3.accept_ra" = 0;
        "net.ipv6.conf.ens3.disable_ipv6" = 1;
      };
      fileSystems."/" = {
        device = "/dev/disk/by-uuid/11111111-2222-3333-4444-555555555555";
        fsType = "ext4";
        options = [ "defaults" ];
        neededForBoot = true;
      };
      networking = {
        hostName = "generic-host";
        useDHCP = false;
        enableIPv6 = true;
        tempAddresses = "disabled";
        interfaces.ens3.useDHCP = true;
        dhcpcd.IPv6rs = false;
        dhcpcd.extraConfig = "noipv6";
      };
      services.openssh = {
        enable = true;
        openFirewall = true;
        settings = {
          PermitRootLogin = "yes";
          PasswordAuthentication = true;
          KbdInteractiveAuthentication = true;
        };
      };
    } (if builtins.elem case [ "plain-declarative-key" "restricted-declarative-key" ] then {
      users.users.root.openssh.authorizedKeys.keys = [
        (if case == "restricted-declarative-key" then restrictedKey else plainKey)
      ];
      users.users.operator = {
        isNormalUser = true;
        extraGroups = [ "wheel" ];
        openssh.authorizedKeys.keys = [ plainKey ];
      };
    } else if case == "custom-unit" then {
      systemd.services.legacy-app = {
        wantedBy = [ "multi-user.target" ];
        serviceConfig.ExecStart = "${pkgs.coreutils}/bin/sleep infinity";
      };
    } else if case == "qemu-agent" then {
      services.qemuGuest.enable = true;
    } else if case == "cloud-init" then {
      services.cloud-init.enable = true;
      networking.interfaces = lib.mkForce { };
    } else if case == "auto-resize" then {
      fileSystems."/".autoResize = true;
    } else { }) ];
  };
  projection = import ../installer/legacy-projection.nix ({
    inherit evaluated nixpkgs;
  } // (if hostingerCase then { reviewedMachine = hostingerMachine; } else { }));
in
assert builtins.hasAttr case expectedReasons;
# Restricted source authority stays verbatim local evidence. This assertion does
# not admit it for runtime retention or unrestricted human access.
assert case != "restricted-declarative-key"
  || projection.ssh.public_authorizations.root.public_keys == [ restrictedKey ];
assert if expectedReasons.${case} == null
  then projection.unsupported == [ ] && projection.machine != null
  else builtins.elem expectedReasons.${case} projection.unsupported && projection.machine == null;
projection
