// Command logging is booth-logging's entrypoint: the read-only query API behind the
// native log viewer (ADR 0015). Loki and the node-level collector that feeds it (ADR 0022)
// are separate workloads in the same Helm chart; this process never ingests anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/projectbooth/booth-logging/internal/api"
	"github.com/projectbooth/booth-logging/internal/auth"
	"github.com/projectbooth/booth-logging/internal/config"
	"github.com/projectbooth/booth-logging/internal/jwksfetch"
	"github.com/projectbooth/booth-logging/internal/loki"
)

const shutdownTimeout = 10 * time.Second

func main() {
	// JSON lines on stdout: what ADR 0022 asks every module to do, and what lets Loki
	// detect this service's own levels. The standard logger (used by internal/auth) is
	// routed through the same handler.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) > 1 && os.Args[1] == "fetch-jwks" {
		if err := fetchJWKS(os.Args[2:]); err != nil {
			slog.Error("fetch-jwks failed", "error", err.Error())
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if len(cfg.AccessWorkspaces) == 0 {
		slog.Warn("log access is open to the owner of ANY workspace: logs are cluster-wide, so every workspace owner can read every tenant's log lines. Set access.workspaces in the chart to restrict it (docs/decisions/0002).")
	} else {
		slog.Info("log access restricted to owners of designated workspaces", "workspaces", cfg.AccessWorkspaces)
	}

	verifier, err := auth.NewVerifier(ctx, cfg.OIDC)
	if err != nil {
		return fmt.Errorf("creating OIDC verifier: %w", err)
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewRouter(api.Deps{
			Verifier:      verifier,
			Loki:          loki.New(cfg.LokiURL, nil),
			Access:        api.AccessPolicy{Workspaces: cfg.AccessWorkspaces},
			Retention:     cfg.Retention,
			MaxQueryRange: cfg.MaxQueryRange,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          log.New(os.Stderr, "", 0),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	slog.Info("booth-logging listening", "addr", cfg.HTTPAddr, "loki", cfg.LokiURL, "retention", cfg.Retention.String())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// fetchJWKS is the Grafana pod's init container (ADR 0076): it writes booth-core's
// iframe-identity JWKS where Grafana's jwk_set_file reads it. See internal/jwksfetch for why.
func fetchJWKS(args []string) error {
	fs := flag.NewFlagSet("fetch-jwks", flag.ContinueOnError)
	issuer := fs.String("issuer", "", "booth-core's iframe-identity issuer URL (exact)")
	out := fs.String("out", "", "file to write the JWKS to")
	wait := fs.Duration("wait", 5*time.Minute, "how long to keep retrying while core is unreachable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *issuer == "" || *out == "" {
		return errors.New("-issuer and -out are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()
	keys, err := jwksfetch.FetchWithRetry(ctx, &http.Client{Timeout: 10 * time.Second}, *issuer, 3*time.Second,
		func(f string, a ...any) { slog.Warn(fmt.Sprintf(f, a...)) })
	if err != nil {
		return err
	}
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, keys, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	slog.Info("wrote booth-core's iframe-identity keys for Grafana", "issuer", *issuer, "out", *out)
	return nil
}
