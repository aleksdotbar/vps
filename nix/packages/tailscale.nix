{ lib, tailscale, buildGo127Module, fetchFromGitHub, fetchpatch2 }:
(tailscale.override { buildGoModule = buildGo127Module; }).overrideAttrs (finalAttrs: previousAttrs: {
  version = "1.104.5";
  src = fetchFromGitHub {
    owner = "tailscale";
    repo = "tailscale";
    tag = "v${finalAttrs.version}";
    hash = "sha256-aq4pBe75wNRFDEa3kSDa/pBLsZws5mOUOjHDUycpmXM=";
  };
  # Backport upstream dependency fixes without changing the shared nixpkgs pin.
  patches = (previousAttrs.patches or [ ]) ++ [
    # x/net HTML parser/renderer fixes.
    (fetchpatch2 {
      url = "https://github.com/tailscale/tailscale/commit/d064d751fb91fa4ffe39dad42c92862320985a0f.patch";
      includes = [ "go.mod" "go.sum" ];
      hash = "sha256-71k2CuZztIMIo6FY+FtwXjrV+vkWhyA07pnn7pQOhTc=";
    })
    # Compression panic fixes.
    (fetchpatch2 {
      url = "https://github.com/tailscale/tailscale/commit/46f938791d5de3534179b48df7db912bc263049f.patch";
      includes = [ "go.mod" "go.sum" ];
      hash = "sha256-oB3vIcFpaOr/V13mvKfsG+VP71b3hJi5p+vWN6yHXn0=";
    })
  ];
  vendorHash = "sha256-LMGCsYR97nhV0PnqtBhAKEg5Mm5kNZMA+KhcupV5+lI=";
  # Preserve function symbols for native binary auditing, without DWARF output.
  ldflags = lib.filter (flag: flag != "-s") previousAttrs.ldflags;
  dontStrip = true;
})
