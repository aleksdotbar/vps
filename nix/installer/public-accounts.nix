# Fixed public account policy shared by source inspection and candidate metadata.
# Keep the native attribute keys for same-source policy comparisons. Credentials,
# password-file references and SSH authority are deliberately excluded.
{ lib, config }:
{
  accounts = builtins.mapAttrs (_: u: {
    inherit (u) name uid group extraGroups home createHome homeMode isNormalUser isSystemUser;
    shell = if u.shell == null then null else if lib.isDerivation u.shell
      then "${u.shell}${u.shell.shellPath}" else u.shell;
  }) config.users.users;
  groups = builtins.mapAttrs (_: g: { inherit (g) name gid members; }) config.users.groups;
}
