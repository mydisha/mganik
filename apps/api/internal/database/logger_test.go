package database_test

import (
	"bytes"
	stdlog "log"
	"strings"
	"testing"

	"gorm.io/gorm/logger"

	"mganik/api/internal/database"
	"mganik/api/testutil"
)

// Log error SQL dari GORM tidak boleh memuat nilai parameter (hash, token, PII).
func TestGormLogger_DoesNotLogParameterValues(t *testing.T) {
	db := testutil.NewDB(t)
	var buf bytes.Buffer
	db.Logger = database.NewGormLogger(&buf)

	const secret = "rahasia-hash-claim-0123456789"
	err := db.Exec(`INSERT INTO users (id, email, name, password_hash) VALUES (gen_random_uuid(), 'x@test.local', 'X', ?)`, secret).Error
	if err == nil {
		t.Fatal("insert dengan hash tidak valid seharusnya ditolak CHECK")
	}
	out := buf.String()
	if !strings.Contains(out, "INSERT INTO users") {
		t.Fatalf("error SQL tidak tercatat: %q", out)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("log memuat nilai parameter: %s", out)
	}

	// Kontrol: logger GORM default memang mencetak nilai parameter, jadi test
	// di atas benar-benar membuktikan efek ParameterizedQueries.
	var control bytes.Buffer
	db.Logger = logger.New(stdlog.New(&control, "", 0), logger.Config{LogLevel: logger.Warn})
	_ = db.Exec(`INSERT INTO users (id, email, name, password_hash) VALUES (gen_random_uuid(), 'x@test.local', 'X', ?)`, secret).Error
	if !strings.Contains(control.String(), secret) {
		t.Fatalf("kontrol tidak mencetak parameter; test tidak bermakna: %q", control.String())
	}
}
