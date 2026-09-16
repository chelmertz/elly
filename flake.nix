{
  description = "elly - prioritized Github PR dashboard";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs =
    { self, nixpkgs }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
    in
    {
      packages.x86_64-linux.default = pkgs.buildGoModule {
        pname = "elly";
        version = self.shortRev or "dirty";
        vendorHash = "sha256-vPgvBemcl0NPW5JJm0nImotK/2tC2JT+K/NHkc8MkVo=";
        src = ./.;
      };

      devShells.x86_64-linux.default = pkgs.mkShell {
        buildInputs = [
          pkgs.go
          # sqlc regenerates internal/storage/{models,query.sql}.go from
          # schema.sql and query.sql. It was missing here until 2026-09-16,
          # so a column added by hand meant editing three generated Scan
          # blocks by hand too - and one of them was missed, which only
          # surfaced as "expected 21 destination arguments in Scan, not 18"
          # at runtime. Run `sqlc generate` instead.
          pkgs.sqlc
          pkgs.pinact
          pkgs.zizmor
          pkgs.golangci-lint
          pkgs.hadolint
        ];
        shellHook = ''
          git config core.hooksPath .githooks
        '';
      };

      overlays.default = final: prev: {
        elly = self.packages.${final.stdenv.hostPlatform.system}.default;
      };
    };
}
