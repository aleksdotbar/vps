# Run from the repository root with read-only/offline Nix evaluation, IFD disabled,
# and --max-jobs 0 --cores 1. Evaluate each named case separately, for example:
# --expr 'let f = builtins.getFlake ("path:" + toString ./nix); in
#   import ./nix/checks/legacy.nix { nixpkgs = f.inputs.nixpkgs.outPath; case = "supported"; }'
# The literal positive must round-trip; each negative must refuse with its fixed
# reason and omit machine input. No build, VM, target inspection or activation.
{ nixpkgs, case ? "supported" }:
let
  expectedReasons = {
    supported = null;
    plain-declarative-key = null;
    restricted-declarative-key = null;
    reviewed-sparse = null;
    reviewed-public-ssh = null;
    reviewed-public-ssh-mismatch = "bootstrap-firewall-policy";
    reviewed-network = "machine-policy";
    reviewed-storage = "boot-policy";
    custom-unit = "native-unit-policy";
    unit-source = "native-unit-policy";
    unit-mask = "native-unit-policy";
    slaac-policy = "sysctl-policy";
    qemu-agent = "native-unit-policy";
    cloud-init = "network-declaration";
    custom-activation = "activation-policy";
    custom-pam = "pam-sudo-policy";
    pam-source = "pam-sudo-policy";
    custom-sudo = "pam-sudo-policy";
    custom-sysctl = "sysctl-policy";
    custom-post-boot = "boot-policy";
    custom-pre-switch = "switch-policy";
    sshd-unit = "native-unit-policy";
    hostinger = null;
    hostinger-changed = "native-unit-policy";
  };
  hostingerCase = builtins.elem case [ "hostinger" "hostinger-changed" ];
  hostingerHost = { lib, ... }: {
    imports = [ ../modules/hostinger-template.nix ];
    services.cloud-init.network.enable = lib.mkIf (case == "hostinger-changed") (lib.mkForce false);
  };
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
    } else if case == "unit-source" then {
      systemd.units."nix-daemon.service".unit = lib.mkForce (pkgs.writeText "custom-daemon-unit" "[Service]\nExecStart=/bin/false\n");
    } else if case == "unit-mask" then {
      systemd.units."nix-daemon.service".enable = lib.mkForce false;
    } else if case == "slaac-policy" then {
      boot.kernel.sysctl."net.ipv6.conf.ens3.disable_ipv6" = lib.mkForce 0;
      boot.kernel.sysctl."net.ipv6.conf.ens3.accept_ra" = lib.mkForce 1;
    } else if case == "qemu-agent" then {
      services.qemuGuest.enable = true;
    } else if case == "cloud-init" then {
      services.cloud-init.enable = true;
      networking.interfaces = lib.mkForce { };
    } else if case == "custom-activation" then {
      system.activationScripts.operator-hook.text = "echo fixture";
    } else if case == "custom-pam" then {
      security.pam.services.login.text = lib.mkForce "auth required pam_deny.so";
    } else if case == "pam-source" then {
      environment.etc."pam.d/login".source = lib.mkForce (pkgs.writeText "custom-pam-login" "auth required pam_deny.so\n");
    } else if case == "custom-sudo" then {
      security.sudo.wheelNeedsPassword = false;
    } else if case == "custom-post-boot" then {
      boot.postBootCommands = "echo fixture";
    } else if case == "custom-pre-switch" then {
      system.preSwitchChecks.operator-check = "false";
    } else if case == "sshd-unit" then {
      systemd.services.sshd.serviceConfig.PAMName = "fixture-ssh";
    } else if case == "custom-sysctl" then {
      boot.kernel.sysctl."net.ipv4.ip_forward" = 1;
    } else { }) ];
  };
  inverse = import ../installer/legacy-projection.nix {
    inherit evaluated nixpkgs;
    operatorName = "operator";
  };
  sparseMachine = inverse.machine // {
    boot = inverse.machine.boot // {
      initrd_available_kernel_modules = [ "virtio_pci" "virtio_blk" ];
      initrd_kernel_modules = [ ];
      kernel_modules = [ ];
    };
  };
  reviewedMachine = if case == "reviewed-network" then sparseMachine // {
    network = sparseMachine.network // { nameservers = [ "192.0.2.53" ]; };
  } else if case == "reviewed-storage" then sparseMachine // {
    file_systems = map (fs: fs // {
      device = "/dev/disk/by-uuid/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee";
    }) sparseMachine.file_systems;
  } else sparseMachine;
  projection = if hostingerCase then import ../installer/legacy-projection.nix {
      inherit evaluated nixpkgs;
      operatorName = "operator";
      reviewedMachine = hostingerMachine;
    } else if builtins.elem case [ "reviewed-sparse" "reviewed-network" "reviewed-storage" "reviewed-public-ssh" "reviewed-public-ssh-mismatch" ]
    then import ../installer/legacy-projection.nix {
      inherit evaluated nixpkgs reviewedMachine;
      operatorName = "operator";
      reviewedPublicSSH22 = if case == "reviewed-public-ssh-mismatch" then false
        else if case == "reviewed-public-ssh" then true else null;
    } else inverse;
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
