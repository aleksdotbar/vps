# Adds the panel module next to the host's own /etc/nixos/configuration.nix,
# switches to it, then runs first-time panel setup. Safe to rerun.
set -euo pipefail

rev="@rev@"
marker="# Managed by the panel installer; local settings belong in panel-local.nix."
flake=/etc/nixos/flake.nix

if [ "$(id -u)" != 0 ]; then
  echo "Run as root." >&2
  exit 1
fi
if [ ! -f /etc/nixos/configuration.nix ]; then
  echo "/etc/nixos/configuration.nix is missing; this installer extends an existing NixOS configuration." >&2
  exit 1
fi
if [ -e "$flake" ] && ! grep -qxF "$marker" "$flake"; then
  echo "$flake exists and was not written by this installer; add nixosModules.default from github:aleksdotbar/vps?dir=nix to it yourself." >&2
  exit 1
fi

cat > "$flake.tmp" <<NIX
$marker
{
  inputs.panel.url = "github:aleksdotbar/vps?dir=nix";
  inputs.nixpkgs.follows = "panel/nixpkgs";

  outputs = { nixpkgs, panel, ... }: {
    # A fixed name: the provider hostname can change when NixOS applies its own.
    nixosConfigurations.panel = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [ ./configuration.nix panel.nixosModules.default ]
        ++ nixpkgs.lib.optional (builtins.pathExists ./panel-local.nix) ./panel-local.nix;
    };
  };
}
NIX
mv "$flake.tmp" "$flake"

export NIX_CONFIG="experimental-features = nix-command flakes"
# The lock pins this exact revision; `nix flake update` later follows main.
nix flake lock /etc/nixos --override-input panel "github:aleksdotbar/vps/$rev?dir=nix"
nixos-rebuild switch --flake /etc/nixos#panel

exec /run/current-system/sw/bin/panel setup "$@"
