{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.retail-prices;

  exe = lib.getExe cfg.package;

  postgresUnits = lib.optional config.services.postgresql.enable "postgresql.service";

  # Settings shared by every unit: who runs it, where its state lives, and
  # how it reaches the database.
  commonServiceConfig = {
    User = cfg.user;
    Group = cfg.group;
    WorkingDirectory = cfg.dataDir;
    StateDirectory = lib.mkIf (cfg.dataDir == "/var/lib/retail-prices") "retail-prices";

    NoNewPrivileges = true;
    PrivateTmp = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    ReadWritePaths = [ cfg.dataDir ];
  } // lib.optionalAttrs (cfg.environmentFile != null) {
    EnvironmentFile = cfg.environmentFile;
  };

  commonEnvironment = cfg.environment // lib.optionalAttrs (cfg.databaseUrl != null) {
    DATABASE_URL = cfg.databaseUrl;
  };

  # One oneshot service per retailer: scrape it, then refresh and match its
  # slice of the catalogue (`retail run`).
  mkPipelineService = name: retailerCfg: {
    "retail-prices-pipeline-${name}" = {
      description = "Retail prices pipeline - ${retailerCfg.displayName}";
      requires = [ "retail-prices-migrate.service" ];
      after = [ "retail-prices-migrate.service" "network-online.target" ] ++ postgresUnits;
      wants = [ "network-online.target" ];
      environment = commonEnvironment;
      serviceConfig = commonServiceConfig // {
        Type = "oneshot";
        # Only read when the store has no recorded scrape yet: the lastRun
        # file the old price-scraper module kept (copy it into dataDir when
        # migrating, see README).
        ExecStart = "${exe} run ${name} --legacy-last-run-file=${cfg.dataDir}/lastRun-${name}";
      };
    };
  };

  mkPipelineTimer = name: retailerCfg: {
    "retail-prices-pipeline-${name}" = {
      description = "Retail prices pipeline timer - ${retailerCfg.displayName}";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnCalendar = if retailerCfg.onCalendar != null then retailerCfg.onCalendar else cfg.timer.onCalendar;
        Persistent = cfg.timer.persistent;
        RandomizedDelaySec = cfg.timer.randomizedDelaySec;
      };
    };
  };

  retailerOptions = lib.types.submodule ({ name, ... }: {
    options = {
      displayName = lib.mkOption {
        type = lib.types.str;
        default = name;
        description = "Human-readable name for this retailer, used in unit descriptions.";
      };

      onCalendar = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "*-*-* 06,18:00:00";
        description = ''
          systemd calendar expression for this retailer only. When null, the
          shared `timer.onCalendar` is used.
        '';
      };
    };
  });

  allPipelineUnits = map (name: "retail-prices-pipeline-${name}.service") (builtins.attrNames cfg.retailers);

in
{
  options.services.retail-prices = {

    enable = lib.mkEnableOption "retail-prices: price scraping for Portuguese retailers and the catalogue built from it";

    package = lib.mkPackageOption pkgs "retail-prices" { };

    databaseUrl = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "postgresql:///price_scraper?host=/run/postgresql";
      description = ''
        Postgres connection string, exported as DATABASE_URL. Leave null and
        set DATABASE_URL in `environmentFile` instead when it holds a
        password. The scraper and the catalogue share this one database.
      '';
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        Environment file loaded by every unit, for secrets and scraper
        tuning (`DATABASE_URL`, `PRICE_SCRAPER_MAX_WORKERS`,
        `PRICE_SCRAPER_REQUESTS_PER_SECOND`, …). The old price-scraper's
        `PRICE_SCRAPER_DB_CONNECTION_STRING` is still read when
        `DATABASE_URL` is not set.
      '';
    };

    environment = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = { PRICE_SCRAPER_MAX_WORKERS = "5"; };
      description = "Extra non-secret environment variables for every unit.";
    };

    retailers = lib.mkOption {
      type = lib.types.attrsOf retailerOptions;
      default = {
        continente = { displayName = "Continente"; };
        auchan = { displayName = "Auchan"; };
        pingodoce = { displayName = "Pingo Doce"; };
        celeiro = { displayName = "Celeiro"; };
      };
      description = ''
        Retailers to scrape. Each key is a retailer id understood by
        `retail run` (continente, auchan, pingodoce, celeiro) and gets its
        own `retail-prices-pipeline-<id>` service and timer. Each pipeline
        refreshes the catalogue for its retailer as soon as its scrape ends.
      '';
    };

    timer = {
      onCalendar = lib.mkOption {
        type = lib.types.str;
        default = "*-*-* 06:00:00";
        description = "systemd calendar expression for when each retailer's pipeline runs.";
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

    serve = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Whether to run the catalogue web app (`retail serve`).";
      };

      address = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1:8080";
        description = "Address the web app listens on.";
      };

      trustProxy = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Read the client IP and scheme from X-Forwarded-For/-Proto. Enable
          only when a reverse proxy that sets them is in front of the app.
        '';
      };

      extraArgs = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Extra arguments for `retail serve`.";
      };
    };

    dataDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/retail-prices";
      description = "Working directory for every unit; holds the image cache.";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "retail-prices";
      description = "User account the units run as.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "retail-prices";
      description = "Group the units run as.";
    };
  };

  config = lib.mkIf cfg.enable {

    users.users.${cfg.user} = lib.mkIf (cfg.user == "retail-prices") {
      isSystemUser = true;
      group = cfg.group;
      home = cfg.dataDir;
      description = "Retail prices service user";
    };

    users.groups.${cfg.group} = lib.mkIf (cfg.group == "retail-prices") { };

    systemd.tmpfiles.rules = [
      "d ${cfg.dataDir} 0750 ${cfg.user} ${cfg.group} -"
    ];

    systemd.services = lib.mkMerge [
      {
        # Schema first: every other unit needs it.
        retail-prices-migrate = {
          description = "Retail prices database migrations";
          wantedBy = [ "multi-user.target" ];
          after = [ "network-online.target" ] ++ postgresUnits;
          wants = [ "network-online.target" ];
          requires = postgresUnits;
          environment = commonEnvironment;
          serviceConfig = commonServiceConfig // {
            Type = "oneshot";
            RemainAfterExit = true;
            ExecStart = "${exe} migrate";
          };
        };
      }

      (lib.mkIf cfg.serve.enable {
        retail-prices-serve = {
          description = "Retail prices catalogue web app";
          wantedBy = [ "multi-user.target" ];
          requires = [ "retail-prices-migrate.service" ];
          after = [ "retail-prices-migrate.service" ] ++ postgresUnits;
          environment = commonEnvironment;
          serviceConfig = commonServiceConfig // {
            ExecStart = lib.escapeShellArgs ([
              exe
              "serve"
              "--addr=${cfg.serve.address}"
              "--image-cache-dir=${cfg.dataDir}/imagecache"
            ] ++ lib.optional cfg.serve.trustProxy "--trust-proxy" ++ cfg.serve.extraArgs);
            Restart = "on-failure";
            RestartSec = "5s";
          };
        };
      })

      (lib.mkMerge (lib.mapAttrsToList mkPipelineService cfg.retailers))

      {
        # Convenience: `systemctl start retail-prices-pipeline-all` runs
        # every retailer's pipeline now.
        retail-prices-pipeline-all = {
          description = "Run every retail prices pipeline";
          wants = allPipelineUnits;
          after = allPipelineUnits;
          serviceConfig.Type = "oneshot";
          script = "true";
        };
      }
    ];

    systemd.timers = lib.mkMerge (lib.mapAttrsToList mkPipelineTimer cfg.retailers);
  };
}
