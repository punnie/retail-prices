{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.price-scraper;

  # Build the list of retailer service names for wiring up the aggregate target/timer.
  retailerServiceNames = map (r: "price-scraper-${r}.service") (builtins.attrNames cfg.retailers);

  # Generate a systemd service for a single retailer.
  mkRetailerService = name: retailerCfg: {
    "price-scraper-${name}" = {
      description = "Price Scraper - ${retailerCfg.displayName}";

      serviceConfig = {
        Type = "oneshot";
        User = cfg.user;
        Group = cfg.group;
        WorkingDirectory = cfg.dataDir;
        StateDirectory = lib.optionalString (cfg.dataDir == "/var/lib/price-scraper") "price-scraper";
      } // lib.optionalAttrs (cfg.environmentFile != null) {
        EnvironmentFile = cfg.environmentFile;
      };

      script =
        let
          scraperBin = "${cfg.package}/bin/price-scraper";
          retailerArg = if retailerCfg.scraperName != null then retailerCfg.scraperName else name;
        in
        ''
          LAST_RUN_FILE="${cfg.dataDir}/lastRun-${name}"

          # Determine the since date
          if [ -f "$LAST_RUN_FILE" ]; then
            SINCE_DATE=$(cat "$LAST_RUN_FILE")
            echo "Using last run date from file: $SINCE_DATE"
          else
            # First run — use 20 years ago as fallback
            SINCE_DATE=$(date -d "20 years ago" +%Y-%m-%d)
            echo "First run — using fallback date: $SINCE_DATE"
          fi

          echo "Starting price scraper for ${retailerCfg.displayName} since: $SINCE_DATE"
          CURRENT_DATE=$(date +%Y-%m-%d)

          # Run scraper for this retailer
          if ${scraperBin} ${retailerArg} --since="$SINCE_DATE"; then
            echo "${retailerCfg.displayName} scraper completed successfully"
            echo "$CURRENT_DATE" > "$LAST_RUN_FILE"
            echo "Updated ${retailerCfg.displayName} last run date to: $CURRENT_DATE"
          else
            echo "${retailerCfg.displayName} scraper failed with exit code $?"
            exit 1
          fi
        '';
    };
  };

  # Retailer sub-module type
  retailerOptions = lib.types.submodule {
    options = {
      displayName = lib.mkOption {
        type = lib.types.str;
        description = "Human-readable name for this retailer, used in logs and service descriptions.";
      };

      scraperName = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          The argument passed to the price-scraper binary to identify this retailer.
          When null, the attribute name in `retailers` is used.
        '';
      };
    };
  };

in
{
  options.services.price-scraper = {

    enable = lib.mkEnableOption "price-scraper, a Portuguese retailers price scraper";

    package = lib.mkPackageOption pkgs "price-scraper" { };

    retailers = lib.mkOption {
      type = lib.types.attrsOf retailerOptions;
      default = {
        continente = { displayName = "Continente"; };
        auchan = { displayName = "Auchan"; };
        pingodoce = { displayName = "Pingo Doce"; };
        celeiro = { displayName = "Celeiro"; };
      };
      description = ''
        Set of retailers to scrape. Each key is the retailer identifier (used as
        the CLI argument and in file names) and the value configures display name
        and optional overrides.

        Adding or removing entries here automatically creates or removes the
        corresponding systemd services and wires them into the shared timer.
      '';
    };

    dataDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/price-scraper";
      description = ''
        Directory used as working directory for the scraper and to store state
        files (last-run timestamps, SQLite database when using the default path,
        etc.).
      '';
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        Path to an environment file loaded by all scraper services.
        Use this to provide secrets and configuration such as
        `PRICE_SCRAPER_DB_TYPE`, `PRICE_SCRAPER_DB_PATH`, etc.
      '';
    };

    timer = {
      onCalendar = lib.mkOption {
        type = lib.types.str;
        default = "*-*-* 06:00:00";
        description = "systemd calendar expression controlling when scrapers run.";
      };

      persistent = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether missed runs should be triggered on the next boot.";
      };

      randomizedDelaySec = lib.mkOption {
        type = lib.types.str;
        default = "4h";
        description = "Maximum random delay added to each timer activation.";
      };
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "price-scraper";
      description = "User account under which the scraper services run.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "price-scraper";
      description = "Group under which the scraper services run.";
    };
  };

  config = lib.mkIf cfg.enable {

    # -------------------------------------------------------------------
    # User / group
    # -------------------------------------------------------------------
    users.users.${cfg.user} = lib.mkIf (cfg.user == "price-scraper") {
      isSystemUser = true;
      group = cfg.group;
      home = cfg.dataDir;
      description = "Price Scraper service user";
    };

    users.groups.${cfg.group} = lib.mkIf (cfg.group == "price-scraper") { };

    # -------------------------------------------------------------------
    # Data directory
    # -------------------------------------------------------------------
    systemd.tmpfiles.rules = [
      "d ${cfg.dataDir} 0750 ${cfg.user} ${cfg.group} -"
    ];

    # -------------------------------------------------------------------
    # Per-retailer oneshot services
    # -------------------------------------------------------------------
    systemd.services = lib.mkMerge [
      # Individual retailer services
      (lib.foldl' (acc: name: acc // mkRetailerService name cfg.retailers.${name}) { }
        (builtins.attrNames cfg.retailers)
      )

      # Aggregate service that depends on all retailers
      {
        price-scraper-all = {
          description = "Run All Price Scrapers";
          serviceConfig.Type = "oneshot";
          wants = retailerServiceNames;
          after = retailerServiceNames;
          script = ''
            echo "All price scraper services completed"
          '';
        };
      }
    ];

    # -------------------------------------------------------------------
    # Aggregate target
    # -------------------------------------------------------------------
    systemd.targets.price-scraper-all = {
      description = "All Price Scraper Services";
      wants = retailerServiceNames;
      after = retailerServiceNames;
    };

    # -------------------------------------------------------------------
    # Timer
    # -------------------------------------------------------------------
    systemd.timers.price-scraper-all = {
      description = "Price Scraper Timer";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = cfg.timer.onCalendar;
        Persistent = cfg.timer.persistent;
        RandomizedDelaySec = cfg.timer.randomizedDelaySec;
      };
    };
  };
}
