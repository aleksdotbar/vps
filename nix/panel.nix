# Builds the panel binary with embedded web assets from this repository's source.
{ pkgs, rev }:
let
  inherit (pkgs) lib;
  src = lib.fileset.toSource {
    root = ../panel;
    fileset = lib.fileset.difference ../panel (lib.fileset.unions (map lib.fileset.maybeMissing [
      ../panel/bin ../panel/web/node_modules ../panel/internal/webassets/dist
    ]));
  };
  pnpm = pkgs.pnpm_12;
  # The repository pins pnpm 12.16.1; nixpkgs' pnpm 12 reads the same lockfile.
  pnpmEnv = {
    npm_config_manage_package_manager_versions = "false";
    npm_config_package_manager_strict = "false";
  };
  web = pkgs.stdenvNoCC.mkDerivation (pnpmEnv // {
    pname = "panel-web";
    version = rev;
    inherit src;
    pnpmRoot = "web";
    pnpmDeps = pkgs.fetchPnpmDeps {
      pname = "panel-web";
      version = rev;
      src = src + "/web";
      inherit pnpm;
      fetcherVersion = 4;
      hash = "sha256-AU3SUMsyoiqjm3UsIHltUgLfzvenlOfV8niqvuOOvYI=";
    };
    nativeBuildInputs = [ pkgs.nodejs_24 pnpm pkgs.pnpmConfigHook ];
    buildPhase = ''
      runHook preBuild
      (cd web && pnpm build)
      runHook postBuild
    '';
    installPhase = "cp -r internal/webassets/dist $out";
  });
in
(pkgs.buildGoModule.override { go = pkgs.go_1_27; }) {
  pname = "panel";
  version = rev;
  inherit src;
  vendorHash = "sha256-TiUqSkV++jV+aKhbxgQavOLlPWkRyjMzlTWbhLB1RtA=";
  subPackages = [ "cmd/panel" ];
  tags = [ "webassets" ];
  env.CGO_ENABLED = "0";
  preBuild = "cp -r ${web} internal/webassets/dist";
  ldflags = [ "-s" ];
  doCheck = false;
}
