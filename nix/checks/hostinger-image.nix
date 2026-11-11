# Fixture: the effective configuration of Hostinger's KVM NixOS image, used by
# the legacy projection checks.
{ lib, ... }:
{
  imports = [ ../modules/hostinger-services.nix ];

  boot.loader.grub = {
    enable = true;
    device = "/dev/sda";
    efiSupport = false;
  };
  boot.loader.timeout = 1;
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
    autoResize = true;
  };
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200" "earlyprintk=ttyS0,115200" "consoleblank=0" "memhp_default_state=online" ];

  # cloud-init renders the address configuration on this image.
  services.cloud-init.network.enable = true;
  networking.useDHCP = lib.mkForce false;

  services.openssh.enable = true;
  services.openssh.settings.PermitRootLogin = lib.mkDefault "yes";
  system.stateVersion = "26.05";
}
