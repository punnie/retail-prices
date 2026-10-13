# Example NixOS configuration using the retail-prices module.
#
# In your flake:
#
#   inputs.retail-prices.url = "github:punnie/retail-prices";
#
#   # … then, inside your NixOS configuration:
#   imports = [ inputs.retail-prices.nixosModules.default ];
#   nixpkgs.overlays = [ inputs.retail-prices.overlays.default ];
#
{ config, ... }:

{
  # ── Minimal ──────────────────────────────────────────────────────────
  #
  # Scrapes every retailer daily and refreshes each retailer's slice of the
  # catalogue as soon as its scrape ends; serves the catalogue on
  # 127.0.0.1:8080.
  #
  services.retail-prices = {
    enable = true;

    # DATABASE_URL (and any PRICE_SCRAPER_* tuning), typically provisioned by
    # agenix, sops-nix, or similar.
    environmentFile = config.age.secrets.retailPricesEnvironmentFile.path;
  };

  # ── Customised ───────────────────────────────────────────────────────
  #
  # services.retail-prices = {
  #   enable = true;
  #
  #   # No secret needed with peer authentication on the local socket:
  #   # databaseUrl = "postgresql:///price_scraper?host=/run/postgresql";
  #
  #   retailers = {
  #     continente = { displayName = "Continente"; onCalendar = "*-*-* 06,18:00:00"; };
  #     auchan     = { displayName = "Auchan"; };
  #     pingodoce  = { displayName = "Pingo Doce"; };
  #     celeiro    = { displayName = "Celeiro"; };
  #   };
  #
  #   timer = {
  #     onCalendar         = "*-*-* 06:00:00";
  #     persistent         = true;
  #     randomizedDelaySec = "4h";
  #   };
  #
  #   serve = {
  #     address    = "127.0.0.1:8080";
  #     trustProxy = true; # only behind a reverse proxy, see README
  #   };
  #
  #   environment.PRICE_SCRAPER_REQUESTS_PER_SECOND = "2.0";
  # };
}
