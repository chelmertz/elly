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
        vendorHash = "sha256-YxbXc+IS4FkaskNWTKFJy/vjxB077b1wcSQ66a6elFo=";
        src = ./.;
      };

      overlays.default = final: prev: {
        elly = self.packages.${final.system}.default;
      };
    };
}
