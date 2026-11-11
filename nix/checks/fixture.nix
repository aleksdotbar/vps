{ ... }:
{
  # Evaluation fixture only; never activate it on a server.
  system.activatable = false;
  system.switch.enable = false;
  system.stateVersion = "26.05";
  nixpkgs.hostPlatform = "x86_64-linux";
  boot.loader.grub.enable = false;
  fileSystems."/" = {
    device = "/dev/disk/by-label/foundation-fixture";
    fsType = "ext4";
  };

  services.openssh.settings.AllowUsers = [ "operator" ];
  users.users.operator = {
    isNormalUser = true;
    extraGroups = [ "wheel" ];
    # Public test vector only.
    openssh.authorizedKeys.keys = [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea build-only-public-test-vector"
    ];
  };
}
