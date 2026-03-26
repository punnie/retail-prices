# Example NixOS configuration using the price-scraper module.
#
# Compare this with price-scraper.nix at the project root to see how much
# boilerplate the module eliminates.
#
# In your flake:
#
#   inputs.price-scraper.url = "github:punnie/price-scraper";
#
#   # … then, inside your NixOS configuration:
#   imports = [ inputs.price-scraper.nixosModules.default ];
#
{ config, ... }:

{
  # ── Minimal ──────────────────────────────────────────────────────────
  #
  # This is all you *need*. Everything else has sane defaults.
  #
  services.price-scraper = {
    enable = true;

    # Point to an environment file that contains secrets / config such as
    # PRICE_SCRAPER_DB_TYPE, PRICE_SCRAPER_DB_PATH, etc.
    # Typically provisioned by agenix, sops-nix, or similar.
    environmentFile = config.age.secrets.priceScraperEnvironmentFile.path;
  };

  # ── Customised ───────────────────────────────────────────────────────
  #
  # Uncomment / adjust any of the options below to override the defaults.
  #
  # services.price-scraper = {
  #   enable = true;
  #
  #   # Override the package (e.g. if you don't use the overlay and build
  #   # it yourself).
  #   # package = pkgs.price-scraper;
  #
  #   # Where state files (lastRun-*, databases, …) are kept.
  #   # Default: /var/lib/price-scraper
  #   # dataDir = "/mnt/price-scraper";
  #
  #   # Environment file with secrets.
  #   # environmentFile = config.age.secrets.priceScraperEnvironmentFile.path;
  #
  #   # Choose which retailers to scrape.  The default set includes all
  #   # four supported retailers.  Remove entries you don't care about, or
  #   # add new ones as the scraper gains support for more stores.
  #   retailers = {
  #     continente = { displayName = "Continente"; };
  #     auchan     = { displayName = "Auchan"; };
  #     pingodoce  = { displayName = "Pingo Doce"; };
  #     celeiro    = { displayName = "Celeiro"; };
  #
  #     # If the CLI argument differs from the attribute name you can
  #     # override it:
  #     # pingo = {
  #     #   displayName = "Pingo Doce";
  #     #   scraperName = "pingodoce";
  #     # };
  #   };
  #
  #   # Timer schedule (systemd calendar syntax).
  #   timer = {
  #     onCalendar         = "*-*-* 06:00:00";
  #     persistent         = true;
  #     randomizedDelaySec = "4h";
  #   };
  #
  #   # User / group the services run as.
  #   # When left at the defaults ("price-scraper" / "price-scraper") the
  #   # module creates them automatically as system accounts.
  #   # user  = "price-scraper";
  #   # group = "price-scraper";
  # };
}
