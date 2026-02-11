{
  description = "elly - prioritized Github PR dashboard";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      pkgs = nixpkgs.legacyPackages.x86_64-linux;
    in
    {
      packages.x86_64-linux.default = pkgs.buildGoModule {
        pname = "elly";
        version = self.shortRev or "dirty";
        vendorHash = "sha256-GMVbDKiNB3K/mmwTutB4bVM98j2euivpjmwVvV/H4Kc=";
        src = ./.;
      };

      overlays.default = final: prev: {
        elly = self.packages.${final.system}.default;
      };
    };
}
