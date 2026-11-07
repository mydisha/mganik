// Command api menjalankan backend mGanik.
//
//	api serve                menjalankan HTTP server (default)
//	api migrate [--seed]     menerapkan migrasi database lalu keluar; --seed juga
//	                         mengisi data demo kalau SEED_DEMO=true
//	api seed                 mengisi data demo (wajib SEED_DEMO=true)
//	api seed --print-demo    mencetak kode serial, PIN, dan akun demo ke stdout
//	                         (tanpa koneksi database)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"mganik/api/internal/config"
	"mganik/api/internal/database"
	"mganik/api/internal/router"
	"mganik/api/internal/seed"
	"mganik/api/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// command adalah hasil parsing argumen CLI.
type command struct {
	name      string // serve | migrate | seed
	withSeed  bool   // migrate --seed
	printDemo bool   // seed --print-demo
}

func parseArgs(args []string, stderr io.Writer) (command, error) {
	c := command{name: "serve"}
	if len(args) > 0 {
		c.name, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch c.name {
	case "serve":
	case "migrate":
		fs.BoolVar(&c.withSeed, "seed", false, "isi data demo setelah migrasi (butuh SEED_DEMO=true)")
	case "seed":
		fs.BoolVar(&c.printDemo, "print-demo", false, "cetak kode serial, PIN klaim, dan akun demo ke stdout")
	default:
		return command{}, fmt.Errorf("perintah tidak dikenal %q (pakai: serve | migrate [--seed] | seed [--print-demo])", c.name)
	}
	if err := fs.Parse(args); err != nil {
		return command{}, err
	}
	if fs.NArg() > 0 {
		return command{}, fmt.Errorf("argumen tidak dikenal untuk %s: %v", c.name, fs.Args())
	}
	return c, nil
}

func run(args []string) error {
	c, err := parseArgs(args, os.Stderr)
	if err != nil {
		return err
	}
	cmd, withSeed := c.name, c.withSeed

	// Fixture demo diturunkan dari konstanta; tidak butuh config atau database.
	if c.printDemo {
		return seed.PrintDemo(os.Stdout)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)
	warnDemoOverride(cfg, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := database.Close(db); err != nil {
			log.Warn("menutup database", "error", err)
		}
	}()

	switch cmd {
	case "migrate":
		if err := database.Migrate(ctx, db, log); err != nil {
			return err
		}
		if !withSeed {
			return nil
		}
		if !cfg.Seed.Demo {
			// Migrasi tetap sukses; seed ditolak tanpa persetujuan eksplisit.
			log.Warn("seed demo dilewati: SEED_DEMO bukan true")
			return nil
		}
		return runSeed(ctx, cfg, db, log)
	case "seed":
		return runSeed(ctx, cfg, db, log)
	}
	return serve(ctx, cfg, db, log)
}

// warnDemoOverride menulis satu WARN saat start kalau production berjalan
// dengan izin data/kredensial demo (SEED_DEMO_ALLOW_PRODUCTION=true).
func warnDemoOverride(cfg config.Config, log *slog.Logger) {
	if cfg.DemoOverrideActive() {
		log.Warn("stack demo: kredensial demo publik aktif, jangan dibuka ke jaringan",
			"SEED_DEMO_ALLOW_PRODUCTION", true, "seed_demo", cfg.Seed.Demo,
			"claim_pepper_demo", string(cfg.ClaimPepper) == config.DemoClaimPepper)
	}
}

func runSeed(ctx context.Context, cfg config.Config, db *gorm.DB, log *slog.Logger) error {
	_, err := seed.Run(ctx, db, seed.Options{
		Enabled:      cfg.Seed.Demo,
		Pepper:       cfg.ClaimPepper,
		DemoPassword: cfg.Seed.DemoPassword,
		Log:          log,
	})
	return err
}

func serve(ctx context.Context, cfg config.Config, db *gorm.DB, log *slog.Logger) error {
	engine, err := router.New(router.Deps{Config: cfg, DB: db, Log: log})
	if err != nil {
		return err
	}

	// Sweeper reservasi + dispatcher outbox di proses yang sama (RUN_WORKER).
	// Stop menunggu putaran yang sedang berjalan selesai sebelum DB ditutup.
	if cfg.Inventory.RunWorker {
		w := worker.New(worker.Deps{DB: db, Log: log, Config: cfg.Inventory})
		w.Start(ctx)
		defer w.Stop()
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           engine,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("server berjalan", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server berhenti: %w", err)
	case <-ctx.Done():
	}

	log.Info("mematikan server", "grace", cfg.ShutdownGrace)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
