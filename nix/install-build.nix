# Builds the panel and both host systems on the installing host from this source
# and the reviewed input. One derivation links all three so one out-link roots them.
{ source, inputJSON, sourceSHA }:
let
  nixpkgs = (builtins.getFlake ("path:" + source + "/nix")).inputs.nixpkgs;
  foundation = import (source + "/nix/foundation.nix") { inherit nixpkgs inputJSON sourceSHA; };
in
nixpkgs.legacyPackages.x86_64-linux.linkFarm "panel-install-${sourceSHA}" {
  panel = foundation.panelPackage;
  enrollment = foundation.enrollment.config.system.build.toplevel;
  final = foundation.final.config.system.build.toplevel;
}
