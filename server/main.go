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
	addr     string
	database *pgxpool.Config
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
	database.MaxConns = 4
	database.ConnConfig.ConnectTimeout = 5 * time.Second

	addr := strings.TrimSpace(os.Getenv("FINANCE_HTTP_ADDR"))
	if addr == "" {
		addr = "127.0.0.1:8096"
	}
	return config{addr: addr, database: database}, nil
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
	if len(os.Args) != 1 {
		if len(os.Args) != 3 {
			log.Fatal("usage: import-practice-roster|import-practice-count-opening|import-legacy-catalog|import-organization|import-order-facts <source.json>")
		}
		switch os.Args[1] {
		case "import-practice-roster":
			err = importPracticeFile(ctx, cfg, os.Args[2])
		case "import-practice-count-opening":
			err = importPracticeCountOpening(ctx, cfg, os.Args[2])
		case "import-order-facts":
			err = importSourceFactFile(ctx, cfg, os.Args[2])
		case "import-organization":
			err = importOrganization(ctx, cfg, os.Args[2])
		case "import-legacy-catalog":
			err = importLegacyCatalog(ctx, cfg, os.Args[2])
		default:
			log.Fatal("usage: import-practice-roster|import-practice-count-opening|import-legacy-catalog|import-organization|import-order-facts <source.json>")
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
