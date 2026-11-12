# Fixture: Hostinger's KVM NixOS image configuration as shipped in
# /etc/nixos/configuration.nix (comments trimmed). Evaluation only.
{ lib, pkgs, modulesPath, ... }:
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  boot.loader.grub.enable = true;
  boot.loader.grub.device = "/dev/sda";
  boot.loader.grub.efiSupport = false;
  boot.loader.timeout = 1;

  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
    autoResize = true;
  };
  boot.growPartition = true;
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200" "earlyprintk=ttyS0,115200" "consoleblank=0" "memhp_default_state=online" ];

  services.qemuGuest.enable = true;
  services.cloud-init.enable = true;
  services.cloud-init.network.enable = true;
  services.cloud-init.settings.datasource_list = [ "NoCloud" "ConfigDrive" ];
  services.cloud-init.settings.ssh.emit_keys_to_console = false;
  networking.useDHCP = lib.mkForce false;

  environment.systemPackages = with pkgs; [ cloud-init dnsutils nettools curl wget nano ];
  systemd.services.qemu-guest-agent.environment.PATH =
    lib.mkForce "/run/wrappers/bin:/run/current-system/sw/bin:/bin";

  services.openssh.enable = true;
  services.openssh.settings.PermitRootLogin = lib.mkDefault "yes";

  system.stateVersion = "26.05";
}
