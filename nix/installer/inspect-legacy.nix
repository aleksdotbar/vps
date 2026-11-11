# The root helper evaluates only this packaged entry, with the protected reviewed
# input and the host's own configuration. The input hash binds the packet to the
# recorded build; runtime storage and network checks prove the live host.
let
  nixpkgs = /nix/var/nix/profiles/per-user/root/channels/nixos;
  reviewedBytes = builtins.readFile /var/lib/panel-host/artifact/input.json;
  reviewed = builtins.fromJSON reviewedBytes;
  evaluated = import (nixpkgs + "/nixos") {
    configuration = /etc/nixos/configuration.nix;
    system = "x86_64-linux";
  };
in
(import ./legacy-projection.nix {
  inherit evaluated nixpkgs;
  reviewedMachine = reviewed.machine;
}) // {
  reviewed_input_sha256 = builtins.hashString "sha256" reviewedBytes;
}
