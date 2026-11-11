# The root helper evaluates only this packaged entry, with a protected reviewed
# artifact input and local legacy source. Hash binding does not replace runtime
# evidence, the running-closure comparison or retained-key admission.
let
  nixpkgs = /nix/var/nix/profiles/per-user/root/channels/nixos;
  reviewedBytes = builtins.readFile /var/lib/panel-host/artifact/input.json;
  reviewed = builtins.fromJSON reviewedBytes;
  operatorName = reviewed.operator.name;
  evaluated = import (nixpkgs + "/nixos") {
    configuration = /etc/nixos/configuration.nix;
    system = "x86_64-linux";
  };
in
assert builtins.match "[a-z_][a-z0-9_-]{0,31}" operatorName != null;
(import ./legacy-projection.nix {
  inherit evaluated nixpkgs operatorName;
  reviewedMachine = reviewed.machine;
  reviewedPublicSSH22 = reviewed.bootstrap.public_ssh22;
}) // {
  reviewed_input_sha256 = builtins.hashString "sha256" reviewedBytes;
}
