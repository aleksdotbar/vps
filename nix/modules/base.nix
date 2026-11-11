{ config, lib, ... }:
{
  imports = [ ./access.nix ./network.nix ./runtime.nix ];

  assertions = [
    {
      assertion = config.nixpkgs.hostPlatform.system == "x86_64-linux";
      message = "The foundation currently supports x86_64-linux instances only.";
    }
  ];

  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    allowed-users = [ "root" "@wheel" ];
    trusted-users = [ "root" ];
  };

  services.journald = {
    storage = "persistent";
    extraConfig = ''
      SystemMaxUse=256M
      RuntimeMaxUse=64M
    '';
  };
  system.autoUpgrade.enable = false;
  nix.gc.automatic = false;

  # The target machine supplies its own boot, disk and network configuration.
  time.timeZone = lib.mkDefault "UTC";
}
