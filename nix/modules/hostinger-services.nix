# Services Hostinger's KVM NixOS image runs for its panel: provisioning through
# the QEMU guest agent and cloud-init's NoCloud/ConfigDrive datasource.
{ lib, pkgs, modulesPath, ... }:
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  services.qemuGuest.enable = true;
  services.cloud-init.enable = true;
  services.cloud-init.settings.datasource_list = [ "NoCloud" "ConfigDrive" ];
  # The NixOS package lacks the console host-key helper this module would call.
  services.cloud-init.settings.ssh.emit_keys_to_console = false;
  boot.growPartition = true;

  # hPanel runs bare-name commands (sh, cloud-init, chpasswd, dig) through the
  # agent, which resolves them on the agent's own PATH.
  environment.systemPackages = with pkgs; [ cloud-init dnsutils nettools curl wget nano ];
  systemd.services.qemu-guest-agent.environment.PATH =
    lib.mkForce "/run/wrappers/bin:/run/current-system/sw/bin:/bin";
}
