{ nixpkgs, instance }:
let
  inherit (nixpkgs) lib;
  pkgs = nixpkgs.legacyPackages.x86_64-linux;
  canBuild = module:
    (builtins.tryEval ((instance.extendModules { modules = [ module ]; }).config.system.build.toplevel.drvPath)).success;
  refusesUnsafeOperator = lib.all (module: !(canBuild module)) [
    { services.openssh.settings.AllowUsers = lib.mkForce [ ]; }
    { users.users.operator.openssh.authorizedKeys.keys = lib.mkForce [ ]; }
    { users.users.operator.enable = false; }
    { users.users.operator.name = "other-login"; }
  ];
in
assert lib.assertMsg (canBuild { }) "The declared fixture operator must remain buildable.";
assert lib.assertMsg refusesUnsafeOperator "An absent, keyless, disabled or renamed operator must not pass the SSH admission guard.";
pkgs.runCommand "foundation-operator-admission" { } ''
  touch "$out"
''
