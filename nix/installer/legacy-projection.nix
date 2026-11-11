# Internal native projection. The target entry fixes source/config paths; fixtures
# may supply an evaluated native configuration without exposing a path/RPC API.
# Evidence stays target-local: authorized-key lines/references are unvalidated and
# may contain options/comments. A native SSH reader must reject unsupported options
# before retaining authority; never return this packet wholesale over RPC.
{ evaluated, nixpkgs, operatorName, reviewedMachine ? null, reviewedPublicSSH22 ? null }:
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
  operatorExists = builtins.hasAttr operatorName c.users.users;
  # Evaluate reviewed intent under the same existing native defaults. Raw inverse
  # module lists cannot be compared to sparse caller input by JSON hash.
  baselineMachine = if reviewedMachine == null then machine else reviewedMachine;
  baseline = import (nixpkgs + "/nixos") {
    system = "x86_64-linux";
    configuration = {
      imports = if hostinger then [ ../modules/hostinger-template.nix ]
        else [ (import ../modules/machine.nix baselineMachine) ];
      users.mutableUsers = true;
      users.users = {
        root.openssh.authorizedKeys = {
          inherit (c.users.users.root.openssh.authorizedKeys) keys keyFiles;
        };
      } // lib.optionalAttrs operatorExists {
        ${operatorName} = {
          isNormalUser = true;
          extraGroups = [ "wheel" ];
          uid = c.users.users.${operatorName}.uid;
          openssh.authorizedKeys = {
            inherit (c.users.users.${operatorName}.openssh.authorizedKeys) keys keyFiles;
          };
        };
      };
      services.openssh = {
        enable = true;
        openFirewall = ssh.openFirewall;
        settings = lib.filterAttrs (name: _: builtins.elem name [
          "PermitRootLogin" "PasswordAuthentication" "KbdInteractiveAuthentication"
          "AuthenticationMethods" "AllowUsers" "AllowAgentForwarding" "X11Forwarding"
        ]) ssh.settings;
      };
    };
  };
  b = baseline.config;
  fsNames = builtins.attrNames c.fileSystems;
  shapeReasons = lib.optionals (machine.system != "x86_64-linux") [ "architecture" ]
    ++ lib.optionals (loader == "unsupported" || (loader == "grub-bios" && builtins.length c.boot.loader.grub.devices != 1)) [ "boot-loader" ]
    ++ lib.optionals (!(c.fileSystems ? "/") || c.fileSystems."/".fsType != "ext4"
      || lib.any (name: name != "/" && !(efi && name == c.boot.loader.efi.efiSysMountPoint && c.fileSystems.${name}.fsType == "vfat")) fsNames) [ "storage-layout" ]
    ++ lib.optionals (lib.any (name: c.fileSystems.${name}.autoFormat || (c.fileSystems.${name}.autoResize && !(hostinger && name == "/"))) fsNames
      || lib.any (s: s.size != null || s.randomEncryption.enable) c.swapDevices) [ "destructive-storage" ]
    ++ lib.optionals (!hostinger && (!network.enableIPv6 || interfaces == [ ]
      || (!network.useNetworkd && !network.dhcpcd.enable))) [ "network-declaration" ];
  identityOf = value: toString value;
  unitPolicy = config: builtins.mapAttrs (_: unit: {
    inherit (unit) enable overrideStrategy wantedBy requiredBy upheldBy aliases text;
    unit = toString unit.unit;
  })
    config.systemd.units;
  pamFiles = config: builtins.mapAttrs (_: file: {
    inherit (file) enable target text mode uid gid user group;
    source = toString file.source;
  })
    (lib.filterAttrs (name: _: lib.hasPrefix "pam.d/" name) config.environment.etc);
  publicAccounts = config: import ./public-accounts.nix { inherit lib config; };
  accounts = config: (publicAccounts config).accounts;
  groups = config: (publicAccounts config).groups;
  networkIdentity = config: {
    inherit (config.networking) hostName domain nameservers search;
  };
  policyReasons = lib.optionals (
      reviewedPublicSSH22 != null && ssh.openFirewall != reviewedPublicSSH22
    ) [ "bootstrap-firewall-policy" ]
    ++ lib.optionals (
      c.system.stateVersion != b.system.stateVersion
      || (hostinger && reviewedMachine.state_version != c.system.stateVersion)
      || networkIdentity c != networkIdentity b) [ "machine-policy" ]
    ++ lib.optionals (
      identityOf c.system.build.kernel != identityOf b.system.build.kernel
      || identityOf c.system.build.initialRamdisk != identityOf b.system.build.initialRamdisk
      || identityOf c.system.build.installBootLoader != identityOf b.system.build.installBootLoader
      || identityOf c.system.build.bootStage2 != identityOf b.system.build.bootStage2
      || c.boot.kernelParams != b.boot.kernelParams) [ "boot-policy" ]
    ++ lib.optionals (unitPolicy c != unitPolicy b) [ "native-unit-policy" ]
    ++ lib.optionals (ssh.settings != b.services.openssh.settings
      || ssh.extraConfig != b.services.openssh.extraConfig
      || ssh.authorizedKeysFiles != b.services.openssh.authorizedKeysFiles
      || (ssh.settings ? AuthenticationMethods && (
        !(builtins.elem ssh.settings.AuthenticationMethods [ "any" "publickey" ])
        || (ssh.settings.AuthenticationMethods == "publickey" && !(
          ssh.settings ? PasswordAuthentication && ssh.settings ? KbdInteractiveAuthentication
          && !ssh.settings.PasswordAuthentication && !ssh.settings.KbdInteractiveAuthentication))))) [ "ssh-policy" ]
    ++ lib.optionals (pamFiles c != pamFiles b
      || c.security.sudo.configFile != b.security.sudo.configFile
      || c.security.sudo.enable != b.security.sudo.enable) [ "pam-sudo-policy" ]
    ++ lib.optionals (!c.users.mutableUsers || accounts c != accounts b || groups c != groups b) [ "account-policy" ]
    ++ lib.optionals (c.boot.kernel.sysctl != b.boot.kernel.sysctl) [ "sysctl-policy" ]
    ++ lib.optionals (c.system.activationScripts != b.system.activationScripts) [ "activation-policy" ]
    ++ lib.optionals (
      c.system.preSwitchChecksScript != b.system.preSwitchChecksScript
      || c.system.switch.enable != b.system.switch.enable
      || c.system.switch.inhibitors != b.system.switch.inhibitors
      || c.system.systemBuilderCommands != b.system.systemBuilderCommands
      || c.system.activatableSystemBuilderCommands != b.system.activatableSystemBuilderCommands) [ "switch-policy" ]
    ++ lib.optionals (c.specialisation != { }) [ "specialisation" ]
    ++ lib.optionals (network.hosts != b.networking.hosts || network.extraHosts != b.networking.extraHosts) [ "name-resolution-policy" ];
  reasons = shapeReasons ++ lib.optionals (shapeReasons == [ ]) policyReasons;
in
{
  schema_version = 1;
  system_path = toString evaluated.system;
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
