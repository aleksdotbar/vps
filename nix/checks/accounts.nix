# Evaluate with a real lib.foundation enrollment/final system and operator name.
# This checks the rendered candidate boundary, not account activation or password
# preservation. Sentinel credentials are fixture data and must never be emitted.
{ system, operatorName }:
let
  sentinel = "PANEL_ACCOUNT_CREDENTIAL_SENTINEL";
  instance = system.extendModules { modules = [ {
    users.users.root.hashedPassword = sentinel;
    users.users.${operatorName}.initialPassword = sentinel;
    users.users.disabled-fixture = { enable = false; isSystemUser = true; group = "nogroup"; };
  } ]; };
  c = instance.config;
  lib = instance.pkgs.lib;
  text = c.environment.etc."panel-host.json".text;
  # Parsing the rendered bytes for a fixture must not ask Nix to interpret the
  # shell paths' derivation context as another store operation.
  metadata = builtins.fromJSON (builtins.unsafeDiscardStringContext text);
  accountFields = [ "createHome" "extraGroups" "group" "home" "homeMode"
    "isNormalUser" "isSystemUser" "name" "shell" "uid" ];
  accountMatches = record:
    let u = c.users.users.${record.name}; in
    builtins.attrNames record == accountFields
    && record.uid == u.uid && record.group == u.group
    && record.extraGroups == u.extraGroups && record.home == u.home
    && record.createHome == u.createHome && record.homeMode == u.homeMode
    && record.isNormalUser == u.isNormalUser && record.isSystemUser == u.isSystemUser
    && record.shell == (if u.shell == null then null else if lib.isDerivation u.shell
      then "${u.shell}${u.shell.shellPath}" else u.shell);
  groupMatches = record:
    let g = c.users.groups.${record.name}; in
    builtins.attrNames record == [ "gid" "members" "name" ]
    && record.gid == g.gid && record.members == g.members;
in
assert map (u: u.name) metadata.accounts == builtins.attrNames
  (lib.filterAttrs (_: u: u.enable) c.users.users);
assert map (g: g.name) metadata.groups == builtins.attrNames c.users.groups;
assert lib.all accountMatches metadata.accounts && lib.all groupMatches metadata.groups;
assert metadata.mutable_users;
assert !(lib.hasInfix sentinel text);
{
  inherit (metadata) role stage;
  account_count = builtins.length metadata.accounts;
  group_count = builtins.length metadata.groups;
  credential_fields_excluded = true;
}
