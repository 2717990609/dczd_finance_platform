package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadConfigRejectsWrongDatabase(t *testing.T) {
	t.Setenv("FINANCE_DATABASE_URL", "postgres://dev@localhost/other_db?sslmode=disable")
	t.Setenv("FINANCE_EXPECTED_DATABASE", "finance_dev")

	if _, err := readConfig(); !errors.Is(err, errWrongDatabase) {
		t.Fatalf("readConfig error = %v, want errWrongDatabase", err)
	}
}

func TestReadConfigDefaultsToLoopback(t *testing.T) {
	t.Setenv("FINANCE_DATABASE_URL", "postgres://dev@localhost/finance_dev?sslmode=disable")
	t.Setenv("FINANCE_EXPECTED_DATABASE", "finance_dev")
	t.Setenv("FINANCE_HTTP_ADDR", "")

	cfg, err := readConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:8096" || cfg.database.ConnConfig.Database != "finance_dev" {
		t.Fatalf("unexpected address or database: %s, %s", cfg.addr, cfg.database.ConnConfig.Database)
	}
}

func TestHealthDoesNotNeedDatabase(t *testing.T) {
	handler := newHandler(func(context.Context) error {
		t.Fatal("liveness should not ping the database")
		return nil
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestReadinessReturnsUnavailableWithoutLeakingConnectionDetails(t *testing.T) {
	const privateError = "private database connection details"
	handler := newHandler(func(context.Context) error { return errors.New(privateError) })
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), privateError) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestReadinessPingsDatabase(t *testing.T) {
	calls := 0
	handler := newHandler(func(context.Context) error {
		calls++
		return nil
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, ping calls = %d", response.Code, calls)
	}
}
