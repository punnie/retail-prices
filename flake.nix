{
  description = "Retail prices analysis platform";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
      nixpkgsFor = forAllSystems (system: import nixpkgs { inherit system; });
    in
    {
      nixosModules.default = import ./nix/module.nix;

      overlays.default = final: prev: {
        retail-prices = final.callPackage ./nix/package.nix { };
      };

      packages = forAllSystems (system:
        let
          retail-prices = nixpkgsFor.${system}.callPackage ./nix/package.nix { };
        in
        {
          default = retail-prices;
          inherit retail-prices;
        }
      );

      devShells = forAllSystems (system:
        let
          pkgs = nixpkgsFor.${system};
          pg = pkgs.postgresql_16;

          # Project-local Postgres cluster: no daemon, no docker. Everything lives
          # under .pgdata/ (gitignored) and listens only on a unix socket in-repo.
          pgdata = "$PWD/.pgdata";
          pgsocketdir = "$PWD/.pgrun";
          pgdb = "price_scraper";

          db-init = pkgs.writeShellScriptBin "db-init" ''
            set -euo pipefail
            if [ -d "${pgdata}" ]; then
              echo "db-init: ${pgdata} already exists, skipping initdb" >&2
              exit 0
            fi
            mkdir -p "${pgsocketdir}"
            ${pg}/bin/initdb -D "${pgdata}" -U "$USER" -A trust --no-instructions
            echo "unix_socket_directories = '${pgsocketdir}'" >> "${pgdata}/postgresql.conf"
          '';

          db-start = pkgs.writeShellScriptBin "db-start" ''
            set -euo pipefail
            mkdir -p "${pgsocketdir}"
            if ${pg}/bin/pg_ctl -D "${pgdata}" status >/dev/null 2>&1; then
              echo "db-start: already running" >&2
              exit 0
            fi
            ${pg}/bin/pg_ctl -D "${pgdata}" -l "${pgdata}/postgres.log" -o "-h \"\" -k ${pgsocketdir}" start
            ${pg}/bin/psql -h "${pgsocketdir}" -U "$USER" -d postgres -tc \
              "SELECT 1 FROM pg_database WHERE datname = '${pgdb}'" | grep -q 1 || \
              ${pg}/bin/createdb -h "${pgsocketdir}" -U "$USER" "${pgdb}"
          '';

          db-stop = pkgs.writeShellScriptBin "db-stop" ''
            set -euo pipefail
            ${pg}/bin/pg_ctl -D "${pgdata}" stop -m fast
          '';

          db-restore = pkgs.writeShellScriptBin "db-restore" ''
            set -euo pipefail
            if [ $# -ne 1 ]; then
              echo "usage: db-restore <dump-file>" >&2
              exit 1
            fi
            dump="$1"
            toclist=$(mktemp)
            trap 'rm -f "$toclist"' EXIT
            ${pg}/bin/pg_restore -l "$dump" | grep -v pg_repack > "$toclist"
            ${pg}/bin/pg_restore -L "$toclist" --no-owner --no-privileges -j4 \
              -h "${pgsocketdir}" -U "$USER" -d "${pgdb}" "$dump"
            echo "db-restore: done" >&2
          '';

          db-psql = pkgs.writeShellScriptBin "db-psql" ''
            exec ${pg}/bin/psql -h "${pgsocketdir}" -U "$USER" -d "${pgdb}" "$@"
          '';
        in
        {
          default = pkgs.mkShell {
            buildInputs = [
              pkgs.go
              pkgs.gopls
              pkgs.gotools
              pkgs.go-tools
              pkgs.delve
              pg
              pkgs.sqlc
              pkgs.git
              db-init
              db-start
              db-stop
              db-restore
              db-psql
            ];

            shellHook = ''
              export PGDATA="${pgdata}"
              export PGHOST="${pgsocketdir}"
              export PGDATABASE="${pgdb}"
              export PGUSER="$USER"
              export DATABASE_URL="postgresql:///${pgdb}?host=${pgsocketdir}"

              echo "Retail Prices Development Environment"
              echo "Go version: $(go version)"
              echo ""
              echo "Postgres (project-local cluster, no daemon):"
              echo "  db-init      - initialize the cluster (first time only)"
              echo "  db-start     - start postgres, create the ${pgdb} database"
              echo "  db-stop      - stop postgres"
              echo "  db-restore   - restore a pg_dump custom-format file into ${pgdb}"
              echo "  db-psql      - open psql against ${pgdb}"
              echo ""
              echo "  DATABASE_URL=$DATABASE_URL"
            '';
          };
        }
      );
    };
}
