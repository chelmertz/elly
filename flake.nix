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
        elly = self.packages.${final.system}.default;
      };
    };
}
