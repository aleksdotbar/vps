{
  description = "Self-hosted deployment panel for NixOS hosts";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs, ... }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
      rev = self.rev or "";
      module = import ./modules/default.nix { rev = if rev == "" then "dirty" else rev; };
      panel = import ./panel.nix {
        inherit pkgs;
        rev = if rev == "" then "dirty" else rev;
      };
      install = pkgs.writeShellApplication {
        name = "panel-install";
        runtimeInputs = [ pkgs.coreutils pkgs.gnugrep pkgs.nix pkgs.nixos-rebuild-ng ];
        text = if rev == "" then ''
          echo "Install from an exact pushed commit: nix run 'github:aleksdotbar/vps/COMMIT?dir=nix#install'" >&2
          exit 1
        '' else builtins.replaceStrings [ "@rev@" ] [ rev ] (builtins.readFile ./install.sh);
      };
    in
    {
      nixosModules.default = module;

      checks.x86_64-linux = {
        hostinger-module = import ./checks/hostinger.nix { inherit nixpkgs module; };
        runtime-admission = import ./checks/runtime.nix {
          inherit nixpkgs;
          instance = nixpkgs.lib.nixosSystem {
            system = "x86_64-linux";
            modules = [ ./checks/hostinger-image.nix module ];
          };
        };
        tunnel-admission = import ./checks/tunnel.nix { inherit nixpkgs; };
      };

      apps.x86_64-linux.install = {
        type = "app";
        program = "${install}/bin/panel-install";
      };

      packages.x86_64-linux = {
        inherit panel;
        docker-network-vm-test = import ./tests/docker-network.nix { inherit pkgs; };
      };
    };
}
