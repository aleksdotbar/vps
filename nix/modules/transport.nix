{ config, pkgs, ... }:
{
  services.tailscale = {
    enable = true;
    package = pkgs.callPackage ../packages/tailscale.nix { };
    openFirewall = false;
    useRoutingFeatures = "none";
    disableTaildrop = true;
    extraSetFlags = [
      "--ssh=false"
      "--accept-dns=false"
      "--accept-routes=false"
      "--netfilter-mode=off"
    ];
  };
  assertions = [
    {
      assertion = config.services.tailscale.authKeyFile == null;
      message = "Foundation enrollment is explicit; keep its credential outside Nix inputs and do not configure tailscale.authKeyFile.";
    }
  ];
}
