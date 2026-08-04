{
  description = "Development environment for lovely-ghostwriter";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { nixpkgs, ... }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.buildGoModule {
            pname = "lovely-ghostwriter";
            version = "0.1.0-dev";
            src = ./.;
            vendorHash = "sha256-QWBsbJcrx8ojWWyMuj0H0gpqvsv82o77MZQvHcRN69k=";
            subPackages = [ "cmd/lovely-ghostwriter" ];
            ldflags = [ "-s" "-w" "-X main.version=0.1.0-dev" ];
          };
        });

      devShells = forAllSystems (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = pkgs.mkShell {
            packages = [ pkgs.go pkgs.gopls pkgs.gotools ];
          };
        });
    };
}
