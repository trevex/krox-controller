{
  description = "krox-controller: KRO-as-library Flux-style deployment controller";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        goOverlay = final: prev: { go = prev.go_1_26; };
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ goOverlay ];
        };
      in {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go gopls gotools go-tools
            kubebuilder
            kubernetes-controller-tools  # controller-gen
            kustomize
            golangci-lint
            gofumpt
            kind
            kubectl
          ];
          shellHook = ''
            echo "Go      $(go version | cut -d' ' -f3)"
            echo "kubebuilder $(kubebuilder version 2>&1 | head -1 || echo n/a)"
            echo "kind        $(kind version 2>&1 | head -1)"
          '';
        };
      });
}
