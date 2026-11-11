# The panel binary built from source, plus the Nix files its root installer
# evaluates on the target, kept in their relative import layout.
{ pkgs, sourceSHA }:
let
  panel = import ./panel.nix { inherit pkgs; rev = sourceSHA; };
in
pkgs.runCommand "panel-${sourceSHA}" { } ''
  install -D -m0755 ${panel}/bin/panel "$out/bin/panel"
  install -D -m0444 ${./installer/inspect-legacy.nix} "$out/share/panel/nix/installer/inspect-legacy.nix"
  install -D -m0444 ${./installer/legacy-projection.nix} "$out/share/panel/nix/installer/legacy-projection.nix"
  install -D -m0444 ${./installer/public-accounts.nix} "$out/share/panel/nix/installer/public-accounts.nix"
  for module in machine hostinger hostinger-services hostinger-template; do
    install -D -m0444 "${./modules}/$module.nix" "$out/share/panel/nix/modules/$module.nix"
  done
''
