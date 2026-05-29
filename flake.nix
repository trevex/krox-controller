{
  description = "Go devshell with latest Go via overlay";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        goOverlay = final: prev: {
          go = prev.go_1_26;
        };

        pkgs = import nixpkgs {
          inherit system;
          overlays = [ goOverlay ];
        };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            go-tools
          ];

          shellHook = ''
            echo "Go $(go version | cut -d' ' -f3) ready"
          '';
        };
      });
}
