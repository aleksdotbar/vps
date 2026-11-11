# Root-only discovery of the existing host's admitted machine and bootstrap SSH
# settings, evaluated from the installing source before any input is written.
# The defaults are the target's fixed paths; fixtures substitute their own.
{ operator
, nixpkgs ? /nix/var/nix/profiles/per-user/root/channels/nixos
, configuration ? /etc/nixos/configuration.nix
}:
let
  evaluated = import (nixpkgs + "/nixos") {
    inherit configuration;
    system = "x86_64-linux";
  };
  c = evaluated.config;
  ssh = c.services.openssh;
  # Hostinger's image keeps its addresses in cloud-init; the installer reads them
  # from the rendered network file and the projection compares the exact image.
  hostinger = c.services.cloud-init.enable && c.services.qemuGuest.enable
    && (c.fileSystems."/".device or null) == "/dev/disk/by-label/nixos"
    && c.boot.loader.grub.devices == [ "/dev/sda" ];
  projection = import ./legacy-projection.nix ({
    inherit evaluated nixpkgs;
    operatorName = operator;
  } // (if hostinger then {
    reviewedMachine = {
      template = "hostinger-kvm";
      state_version = c.system.stateVersion;
      network = null;
    };
  } else { }));
in
# `nix eval --file` applies --argstr only when an attribute is selected.
{
  host = {
  inherit hostinger;
  inherit (projection) unsupported machine;
  bootstrap = {
    public_ssh22 = ssh.openFirewall;
    root_login = ssh.settings.PermitRootLogin;
    password_authentication = ssh.settings.PasswordAuthentication;
    keyboard_interactive_authentication = ssh.settings.KbdInteractiveAuthentication;
  };
  };
}
