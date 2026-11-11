# Internal native projection. The target entry fixes source/config paths; fixtures
# may supply an evaluated native configuration without exposing a path/RPC API.
# It derives a bootable machine description and the declared SSH key paths; it does
# not compare the host's other settings. Never return this packet over RPC.
{ evaluated, nixpkgs, reviewedMachine ? null }:
let
  lib = import (nixpkgs + "/lib");
  c = evaluated.config;
  network = c.networking;
  # Hostinger's image keeps cloud-init addressing outside Nix; runtime inspection
  # proves the reviewed network instead of this source projection.
  hostinger = reviewedMachine != null && (reviewedMachine.template or null) == "hostinger-kvm";
  efi = c.boot.loader.grub.efiSupport || c.boot.loader.systemd-boot.enable;
  loader = if c.boot.loader.systemd-boot.enable then "systemd-boot"
    else if c.boot.loader.grub.enable then (if efi then "grub-efi" else "grub-bios")
    else "unsupported";
  interfaces = builtins.attrNames network.interfaces;
  address = a: { inherit (a) address; prefix_length = a.prefixLength; };
  route = r: {
    inherit (r) address;
    prefix_length = r.prefixLength;
    scope = r.options.scope or "global";
    metric = if r.options ? metric then lib.toInt r.options.metric else null;
    on_link = r.options ? onlink;
  } // lib.optionalAttrs (r.via != null) { via = r.via; };
  gateway = g: if g == null then null else {
    inherit (g) address interface metric;
  } // lib.optionalAttrs (g.source != null) { source = g.source; };
  machine = {
    schema_version = 1;
    system = evaluated.pkgs.stdenv.hostPlatform.system;
    state_version = c.system.stateVersion;
    boot = {
      inherit loader;
      efi_can_touch_variables = c.boot.loader.efi.canTouchEfiVariables;
      efi_install_as_removable = c.boot.loader.grub.efiInstallAsRemovable;
      grub_copy_kernels = c.boot.loader.grub.copyKernels;
      configuration_limit = if loader == "systemd-boot"
        then c.boot.loader.systemd-boot.configurationLimit else c.boot.loader.grub.configurationLimit;
      initrd_systemd = c.boot.initrd.systemd.enable;
      initrd_available_kernel_modules = lib.unique c.boot.initrd.availableKernelModules;
      initrd_kernel_modules = lib.unique c.boot.initrd.kernelModules;
      kernel_modules = lib.unique c.boot.kernelModules;
      console = map (p: lib.removePrefix "console=" p)
        (builtins.filter (lib.hasPrefix "console=") c.boot.kernelParams);
    } // lib.optionalAttrs (loader == "grub-bios" && builtins.length c.boot.loader.grub.devices == 1) {
      grub_device = builtins.head c.boot.loader.grub.devices;
    } // lib.optionalAttrs efi { efi_mount_point = c.boot.loader.efi.efiSysMountPoint; };
    file_systems = map (name:
      let fs = c.fileSystems.${name}; in {
        mount_point = name;
        inherit (fs) device;
        # Native filesystems.nix injects these markers again from neededForBoot
        # and, on Hostinger's root, from autoResize.
        options = let
          injected = lib.optional (name == "/" || fs.neededForBoot) "x-initrd.mount"
            ++ lib.optional (hostinger && name == "/") "x-systemd.growfs";
          kept = builtins.filter (option: !(builtins.elem option injected)) fs.options;
        in if kept == [ ] then [ "defaults" ] else kept;
        fs_type = fs.fsType;
        needed_for_boot = fs.neededForBoot;
      }) (builtins.attrNames c.fileSystems);
    swap = map (s: { inherit (s) device priority; }) c.swapDevices;
    network = {
      backend = if network.useNetworkd then "networkd" else "dhcpcd";
      host_name = network.hostName;
      use_dhcp = network.useDHCP;
      ipv6_mode = if lib.any (name: network.interfaces.${name}.ipv6.addresses != [ ]) interfaces
        then "static" else "disabled";
      inherit (network) nameservers search;
      interfaces = map (name:
        let i = network.interfaces.${name}; in {
          inherit name;
          inherit (i) mtu;
          dhcp = i.useDHCP;
          ipv4_addresses = map address i.ipv4.addresses;
          ipv6_addresses = map address i.ipv6.addresses;
          ipv4_routes = map route i.ipv4.routes;
          ipv6_routes = map route i.ipv6.routes;
        } // lib.optionalAttrs (i.macAddress != null) { mac_address = i.macAddress; }) interfaces;
      default_gateway = gateway network.defaultGateway;
      default_gateway6 = gateway network.defaultGateway6;
    } // lib.optionalAttrs (network.domain != null) { domain = network.domain; };
  };
  ssh = c.services.openssh;
  fsNames = builtins.attrNames c.fileSystems;
  shapeReasons = lib.optionals (machine.system != "x86_64-linux") [ "architecture" ]
    ++ lib.optionals (loader == "unsupported" || (loader == "grub-bios" && builtins.length c.boot.loader.grub.devices != 1)) [ "boot-loader" ]
    ++ lib.optionals (!(c.fileSystems ? "/") || c.fileSystems."/".fsType != "ext4"
      || lib.any (name: name != "/" && !(efi && name == c.boot.loader.efi.efiSysMountPoint && c.fileSystems.${name}.fsType == "vfat")) fsNames) [ "storage-layout" ]
    ++ lib.optionals (lib.any (name: c.fileSystems.${name}.autoFormat || (c.fileSystems.${name}.autoResize && !(hostinger && name == "/"))) fsNames
      || lib.any (s: s.size != null || s.randomEncryption.enable) c.swapDevices) [ "destructive-storage" ]
    ++ lib.optionals (!hostinger && (!network.enableIPv6 || interfaces == [ ]
      || (!network.useNetworkd && !network.dhcpcd.enable))) [ "network-declaration" ];
  publicAccounts = config: import ./public-accounts.nix { inherit lib config; };
  accounts = config: (publicAccounts config).accounts;
  groups = config: (publicAccounts config).groups;
  reasons = shapeReasons;
in
{
  schema_version = 1;
  system_path = toString evaluated.system;
  # system_path and boot_files stay in the packet shape; installers no longer compare them.
  boot_files = {
    kernel = "${c.boot.kernelPackages.kernel}/${c.system.boot.loader.kernelFile}";
    initrd = "${c.system.build.initialRamdisk}/${c.system.boot.loader.initrdFile}";
  };
  unsupported = reasons;
  machine = if reasons != [ ] then null
    else if hostinger then machine // { inherit (reviewedMachine) network template; }
    else machine;
  mutable_users = c.users.mutableUsers;
  accounts = builtins.attrValues (accounts c);
  groups = builtins.attrValues (groups c);
  ssh = {
    authorized_keys_files = ssh.authorizedKeysFiles;
    public_authorizations = builtins.mapAttrs (_: u: {
      public_keys = u.openssh.authorizedKeys.keys;
      public_key_files = map toString u.openssh.authorizedKeys.keyFiles;
    }) c.users.users;
  };
}
