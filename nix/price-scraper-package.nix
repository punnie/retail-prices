# Legacy nix compatibility - allows importing with `import ./. {}`
{ pkgs ? import <nixpkgs> {}
, lib ? pkgs.lib
}:

pkgs.buildGoModule rec {
  pname = "price-scraper";
  version = "0.1.0";
  
  src = pkgs.lib.cleanSourceWith {
    src = ./.;
    filter = path: type:
      let
        baseName = baseNameOf path;
      in
      # Exclude test files that have main functions
      !(pkgs.lib.hasSuffix "test_streaming.go" baseName);
  };
  
  vendorHash = "sha256-GPYi14995zphM1mLmbjTRoMHZGdGjKOjVTdIkRHIQLc=";
  
  # Build flags
  ldflags = [
    "-s"
    "-w"
    "-X main.version=0.1.0"
  ];
  
  # Runtime dependencies
  buildInputs = with pkgs; [
    sqlite
  ];
  
  # Ensure CGO is enabled for sqlite3
  env.CGO_ENABLED = "1";
  
  meta = with pkgs.lib; {
    description = "Portuguese retailers price scraper";
    homepage = "https://github.com/punnie/price-scraper";
    license = licenses.mit; # Adjust if different
    maintainers = [ ];
    platforms = platforms.unix;
    mainProgram = "price-scraper";
  };
}
