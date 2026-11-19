package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errWrongDatabase = errors.New("database URL does not match FINANCE_EXPECTED_DATABASE")

type config struct {
	addr                 string
	database             *pgxpool.Config
	dcaiSyncInterval     time.Duration
	dczdSyncInterval     time.Duration
	draftInterval        time.Duration
	settlementSyncMaxAge time.Duration
	ruleAuth             catalogRuleAuth
	development          bool
}

func readConfig() (config, error) {
	databaseURL := strings.TrimSpace(os.Getenv("FINANCE_DATABASE_URL"))
	expectedDatabase := strings.TrimSpace(os.Getenv("FINANCE_EXPECTED_DATABASE"))
	if databaseURL == "" || expectedDatabase == "" {
		return config{}, errors.New("set FINANCE_DATABASE_URL and FINANCE_EXPECTED_DATABASE")
	}
	database, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return config{}, errors.New("FINANCE_DATABASE_URL is invalid")
	}
	if database.ConnConfig.Database != expectedDatabase {
		return config{}, errWrongDatabase
	}
	// Three order workers may hold advisory locks while reading upstream.
	// Keep three additional connections available for drafts and HTTP requests.
	database.MaxConns = 6
	database.ConnConfig.ConnectTimeout = 5 * time.Second

	addr := strings.TrimSpace(os.Getenv("FINANCE_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8096"
	}
	interval, err := intervalSetting("FINANCE_DCAI_SYNC_INTERVAL")
	if err != nil {
		return config{}, err
	}
	dczdInterval, err := intervalSetting("FINANCE_DCZD_SYNC_INTERVAL")
	if err != nil {
		return config{}, err
	}
	draftInterval, err := intervalSetting("FINANCE_DRAFT_REFRESH_INTERVAL")
	if err != nil {
		return config{}, err
	}
	settlementAge := max(defaultSettlementSyncMaxAge, interval)
	if value := strings.TrimSpace(os.Getenv("FINANCE_SETTLEMENT_SYNC_MAX_AGE")); value != "" {
		settlementAge, err = time.ParseDuration(value)
		if err != nil || settlementAge < time.Second {
			return config{}, errors.New("FINANCE_SETTLEMENT_SYNC_MAX_AGE must be a duration of at least one second")
		}
	}
	ruleAuth, err := readCatalogRuleAuth()
	if err != nil {
		return config{}, err
	}
	development, err := developmentAccess(addr, expectedDatabase)
	if err != nil {
		return config{}, err
	}
	return config{addr: addr, database: database, dcaiSyncInterval: interval, dczdSyncInterval: dczdInterval, draftInterval: draftInterval,
		settlementSyncMaxAge: settlementAge, ruleAuth: ruleAuth, development: development}, nil
}

func intervalSetting(name string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 30 * time.Minute, nil
	}
	interval, err := time.ParseDuration(value)
	if err != nil || interval < 0 || interval > 0 && interval < time.Second {
		return 0, errors.New(name + " must be 0 or a duration of at least one second")
	}
	return interval, nil
}

func newHandler(ping func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ping(ctx); err != nil {
			writeStatus(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	})
	return mux
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func run(ctx context.Context, cfg config) error {
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the database pool")
	}
	defer db.Close()

	return startHTTP(ctx, cfg, db)
}

func main() {
	cfg, err := readConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) > 1 && os.Args[1] == "initialize-finance" {
		if len(os.Args) != 3 && (len(os.Args) != 4 || os.Args[3] != "--apply") {
			log.Fatal("usage: initialize-finance <file.json> [--apply]")
		}
		if err := initializeFinanceFromFile(ctx, cfg, os.Args[2], len(os.Args) == 4); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "adopt-finance-history" {
		mode := ""
		if len(os.Args) == 4 {
			mode = os.Args[3]
		}
		if len(os.Args) != 3 && (len(os.Args) != 4 || mode != "--apply" && mode != "--rows") {
			log.Fatal("usage: adopt-finance-history <file.json> [--rows|--apply]")
		}
		if err := adoptFinanceHistoryFromFile(ctx, cfg, os.Args[2], mode); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sync-dcai-orders" {
		if err := syncOrdersCLI(ctx, cfg, "DCAI", os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "sync-dczd-orders" {
		if err := syncOrdersCLI(ctx, cfg, "DCZD", os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 1 {
		const importUsage = "usage: configure-finance-account|import-practice-roster|import-practice-count-opening|import-product-config|import-organization|import-order-facts|import-sales-context <file.json>"
		if len(os.Args) != 3 {
			log.Fatal(importUsage)
		}
		switch os.Args[1] {
		case "configure-finance-account":
			err = configureFinanceAccount(ctx, cfg, os.Args[2])
		case "import-practice-roster":
			err = importPracticeFile(ctx, cfg, os.Args[2])
		case "import-practice-count-opening":
			err = importPracticeCountOpening(ctx, cfg, os.Args[2])
		case "import-sales-context":
			err = importVIPSalesContext(ctx, cfg, os.Args[2])
		case "import-order-facts":
			err = importSourceFactFile(ctx, cfg, os.Args[2])
		case "import-organization":
			err = importOrganization(ctx, cfg, os.Args[2])
		case "import-product-config":
			err = importProductConfig(ctx, cfg, os.Args[2])
		default:
			log.Fatal(importUsage)
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Printf("finance platform listening on %s", cfg.addr)
	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
