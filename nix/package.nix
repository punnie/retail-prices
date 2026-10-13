# The `retail` binary: scraper, catalogue pipeline and web app.
{ lib, buildGoModule }:

buildGoModule {
  pname = "retail-prices";
  version = "0.5.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../db
      ../internal
    ];
  };

  vendorHash = "sha256-QiQdmoUTq4RIW8gyTHX7fat/1V8/bjPPI3aD2Vy8l34=";

  subPackages = [ "cmd/retail" ];

  # Pure Go since the scraper dropped SQLite.
  env.CGO_ENABLED = "0";

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Price observations from Portuguese retailers, turned into a browsable product catalogue";
    homepage = "https://github.com/punnie/retail-prices";
    platforms = lib.platforms.unix;
    mainProgram = "retail";
  };
}
