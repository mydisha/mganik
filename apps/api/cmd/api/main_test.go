package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"mganik/api/internal/config"
)

func TestWarnDemoOverride(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  config.Config
		warn bool
	}{
		{"production dengan override", config.Config{AppEnv: config.EnvProduction, Seed: config.SeedConfig{AllowProduction: true}}, true},
		{"production tanpa override", config.Config{AppEnv: config.EnvProduction}, false},
		{"development dengan override", config.Config{AppEnv: config.EnvDevelopment, Seed: config.SeedConfig{AllowProduction: true}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			warnDemoOverride(tt.cfg, slog.New(slog.NewTextHandler(&buf, nil)))
			got := strings.Count(buf.String(), "kredensial demo publik aktif")
			if (got == 1) != tt.warn || got > 1 {
				t.Fatalf("jumlah WARN %d, ingin warn=%v: %s", got, tt.warn, buf.String())
			}
			if tt.warn && !strings.Contains(buf.String(), "level=WARN") {
				t.Fatalf("bukan level WARN: %s", buf.String())
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args    []string
		want    command
		wantErr string
	}{
		{args: nil, want: command{name: "serve"}},
		{args: []string{"serve"}, want: command{name: "serve"}},
		{args: []string{"migrate"}, want: command{name: "migrate"}},
		{args: []string{"migrate", "--seed"}, want: command{name: "migrate", withSeed: true}},
		{args: []string{"seed"}, want: command{name: "seed"}},
		{args: []string{"seed", "--print-demo"}, want: command{name: "seed", printDemo: true}},
		{args: []string{"seed", "-print-demo=true"}, want: command{name: "seed", printDemo: true}},
		{args: []string{"hapus"}, wantErr: "perintah tidak dikenal"},
		// Flag hanya berlaku di subcommand-nya.
		{args: []string{"serve", "--seed"}, wantErr: "not defined"},
		{args: []string{"migrate", "--print-demo"}, wantErr: "not defined"},
		{args: []string{"seed", "--seed"}, wantErr: "not defined"},
		{args: []string{"migrate", "lagi"}, wantErr: "argumen tidak dikenal"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			got, err := parseArgs(tt.args, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ingin error %q, dapat %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("dapat %+v, ingin %+v", got, tt.want)
			}
		})
	}
}
