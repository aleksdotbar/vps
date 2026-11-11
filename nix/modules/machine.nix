# Input is canonical JSON admitted by panel/internal/machine. This fixed
# projection is not an importer for arbitrary NixOS modules or attrsets.
machine:
{ config, lib, options, ... }:
let
  boot = machine.boot;
  network = machine.network;
  hostinger = (machine.template or null) == "hostinger-kvm";
  efi = boot.loader != "grub-bios";
  address = value: {
    inherit (value) address;
    prefixLength = value.prefix_length;
  };
  route = value: {
    inherit (value) address;
    prefixLength = value.prefix_length;
    type = "unicast";
    via = value.via or null;
    options = { scope = value.scope; }
      // lib.optionalAttrs (value.metric != null) { metric = toString value.metric; }
      // lib.optionalAttrs value.on_link { onlink = ""; };
  };
  gateway = value:
    if value == null then null else {
      inherit (value) address interface metric;
      source = value.source or null;
    };
in
{
  imports = if hostinger then [ ./hostinger.nix ] else [ ];

  nixpkgs.hostPlatform = machine.system;
  system.stateVersion = machine.state_version;

  boot = {
    initrd = {
      systemd.enable = boot.initrd_systemd;
      availableKernelModules = boot.initrd_available_kernel_modules;
      kernelModules = boot.initrd_kernel_modules or [ ];
    };
    kernelModules = boot.kernel_modules or [ ];
    kernelParams = map (console: "console=${console}") (boot.console or [ ]);
    kernel.sysctl = lib.mkMerge (map (value: {
      "net.ipv6.conf.${value.name}.accept_ra" = 0;
      "net.ipv6.conf.${value.name}.disable_ipv6" =
        if network.ipv6_mode == "disabled" then 1 else 0;
    }) network.interfaces);
    loader = {
      grub = {
        enable = boot.loader != "systemd-boot";
        devices = if boot.loader == "grub-bios" then [ boot.grub_device ] else [ "nodev" ];
        efiSupport = boot.loader == "grub-efi";
        efiInstallAsRemovable = boot.efi_install_as_removable;
        copyKernels = boot.grub_copy_kernels;
      } // lib.optionalAttrs (boot.loader != "systemd-boot") {
        configurationLimit = boot.configuration_limit;
      };
      systemd-boot = {
        enable = boot.loader == "systemd-boot";
        configurationLimit = boot.configuration_limit;
      };
    } // lib.optionalAttrs efi {
      efi = {
        efiSysMountPoint = boot.efi_mount_point;
        canTouchEfiVariables = boot.efi_can_touch_variables;
      };
    };
  };

  fileSystems = builtins.listToAttrs (map (value: {
    name = value.mount_point;
    value = {
      inherit (value) device options;
      fsType = value.fs_type;
      neededForBoot = value.needed_for_boot;
      autoFormat = false;
      # Hostinger plan upgrades enlarge the disk; growing ext4 keeps its data.
      autoResize = hostinger && value.mount_point == "/";
    };
  }) machine.file_systems);
  swapDevices = map (value: {
    inherit (value) device priority;
    size = null;
    randomEncryption.enable = false;
  }) (machine.swap or [ ]);

  networking = {
    hostName = network.host_name;
    domain = network.domain or null;
    useNetworkd = network.backend == "networkd";
    useDHCP = network.use_dhcp;
    # Runtime Docker bridges and loopback retain IPv6 even when the physical
    # interfaces have no IPv6. Preflight rejects globally disabled targets.
    enableIPv6 = true;
    tempAddresses = "disabled";
    nameservers = network.nameservers or [ ];
    search = network.search or [ ];
    defaultGateway = gateway network.default_gateway;
    defaultGateway6 = gateway network.default_gateway6;
    interfaces = builtins.listToAttrs (map (value: {
      name = value.name;
      value = {
        useDHCP = value.dhcp;
        macAddress = value.mac_address or null;
        mtu = value.mtu;
        ipv4 = {
          addresses = map address (value.ipv4_addresses or [ ]);
          routes = map route (value.ipv4_routes or [ ]);
        };
        ipv6 = {
          addresses = map address (value.ipv6_addresses or [ ]);
          routes = map route (value.ipv6_routes or [ ]);
        };
      };
    }) network.interfaces);
    dhcpcd.IPv6rs = false;
    dhcpcd.extraConfig = "noipv6";
  };

  # Static/disabled IPv6 is the admitted subset. Never rely on kernel RA
  # acceptance changing implicitly when Docker enables forwarding.
  systemd.network.networks = lib.mkIf (network.backend == "networkd") (lib.mkMerge [
    (builtins.listToAttrs (map (value: {
      name = "40-${value.name}";
      value = {
        enable = !hostinger;
        DHCP = lib.mkOverride 0 (
          if (if value.dhcp != null then value.dhcp else
            network.use_dhcp && (value.ipv4_addresses or [ ]) == [ ])
          then "ipv4" else "no"
        );
        networkConfig = {
          IPv6AcceptRA = false;
          LinkLocalAddressing = if network.ipv6_mode == "disabled" then "no" else "ipv6";
        };
      };
    }) network.interfaces))
    # Hostinger rollback generations still need cloud-init's 10-cloud-init-<if>.network,
    # so it stays on disk; this earlier-sorting copy of 40-<if> takes the link instead.
    (lib.mkIf hostinger (builtins.listToAttrs (map (value: {
      name = "05-${value.name}";
      # Copy option by option: reading the 40 unit wholesale would recurse.
      value = lib.mapAttrs (option: _: config.systemd.network.networks."40-${value.name}".${option})
        (lib.filterAttrs (option: _: !lib.hasPrefix "_" option && option != "enable")
          (options.systemd.network.networks.type.getSubOptions [ ])) // { enable = true; };
    }) network.interfaces)))
  ]);
}
