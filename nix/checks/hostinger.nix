# The panel module beside Hostinger's image configuration must keep provider
# access and networking intact and expose SSH publicly only by choice.
{ nixpkgs, module }:
let
  inherit (nixpkgs) lib;
  host = publicSSH: lib.nixosSystem {
    system = "x86_64-linux";
    modules = [ ./hostinger-image.nix module { services.vpsPanel.publicSSH = publicSSH; } ];
  };
  open = (host true).config;
  closed = (host false).config;
  sshd = c: c.services.openssh.settings;
  networkFiles = c: builtins.filter (name: lib.hasPrefix "systemd/network/" name && name != "systemd/network/50-tailscale.network")
    (builtins.attrNames c.environment.etc);
in
assert lib.assertMsg ((host true).config.system.build.toplevel.drvPath != "") "The Hostinger host must evaluate.";
assert lib.assertMsg ((sshd open).AllowUsers or null == null) "No global AllowUsers may lock out provider logins.";
assert lib.assertMsg ((sshd open).PermitRootLogin == "yes" && (sshd open).PasswordAuthentication != false)
  "The provider's root and password login must stay as configured.";
assert lib.assertMsg (builtins.elem 22 open.networking.firewall.allowedTCPPorts) "Public SSH stays open by default.";
assert lib.assertMsg (!(builtins.elem 22 closed.networking.firewall.allowedTCPPorts)
  && builtins.elem 22 closed.networking.firewall.interfaces.tailscale0.allowedTCPPorts)
  "publicSSH = false must keep SSH only on Tailscale.";
assert lib.assertMsg (!(builtins.elem 443 open.networking.firewall.allowedTCPPorts)) "Panel HTTPS must stay private.";
assert lib.assertMsg (networkFiles open == [ ] && !open.networking.useNetworkd && open.services.cloud-init.network.enable)
  "The provider's cloud-init networking must stay untouched.";
nixpkgs.legacyPackages.x86_64-linux.emptyFile
