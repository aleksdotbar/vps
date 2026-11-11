{
  description = "NixOS access and security foundation";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs, ... }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
      rev = self.rev or "";
      panel = import ./panel.nix {
        inherit pkgs;
        rev = if rev == "" then "dirty" else rev;
      };
      # Installation builds the host from the exact fetched source; a dirty or
      # unpinned tree has no revision to record.
      install = pkgs.writeShellScript "panel-install" (if rev == "" then ''
        echo "Install from an exact pushed commit: nix run 'github:OWNER/vps/COMMIT?dir=nix#install'" >&2
        exit 1
      '' else ''
        exec ${panel}/bin/panel install --source ${self.sourceInfo.outPath} --source-sha ${rev} "$@"
      '');
      fixture = nixpkgs.lib.nixosSystem {
        modules = [ ./modules/base.nix ./checks/fixture.nix ];
      };
    in
    {
      nixosModules.foundation = import ./modules/base.nix;
      nixosModules.enrollment = import ./modules/enrollment.nix;
      nixosModules.panel = import ./modules/panel.nix;

      lib.foundation = { inputJSON, sourceSHA }: import ./foundation.nix {
        inherit nixpkgs inputJSON sourceSHA;
      };

      checks.x86_64-linux = {
        operator-admission = import ./checks/access.nix {
          inherit nixpkgs;
          instance = fixture;
        };
        runtime-admission = import ./checks/runtime.nix {
          inherit nixpkgs;
          instance = fixture;
        };
        network-admission = import ./checks/network.nix {
          inherit nixpkgs;
          instance = fixture;
        };
        tunnel-admission = import ./checks/tunnel.nix {
          inherit nixpkgs;
        };
      };

      apps.x86_64-linux.install = {
        type = "app";
        program = toString install;
      };

      packages.x86_64-linux = {
        inherit panel;
        initial-host-vm-test = import ./tests/initial-host.nix {
          pkgs = nixpkgs.legacyPackages.x86_64-linux;
        };
        docker-network-vm-test = import ./tests/docker-network.nix {
          pkgs = nixpkgs.legacyPackages.x86_64-linux;
        };
      };
    };
}
