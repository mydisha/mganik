package seed_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"

	"gorm.io/gorm"

	"mganik/api/internal/config"
	"mganik/api/internal/pii"
	"mganik/api/internal/seed"
	"mganik/api/testutil"
)

func demoSigningKeyEnc(t *testing.T, db *gorm.DB) []byte {
	t.Helper()
	var row struct{ SigningKeyEnc []byte }
	if err := db.Raw(`SELECT signing_key_enc FROM pos_devices WHERE id = ?`, seed.PosDeviceID()).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.SigningKeyEnc
}

func openSigningKey(t *testing.T, master string, enc []byte) []byte {
	t.Helper()
	c, err := pii.NewSigningKeyCipher([]byte(master))
	if err != nil {
		t.Fatal(err)
	}
	id := seed.PosDeviceID()
	key, err := c.Open(enc, id[:])
	if err != nil {
		t.Fatalf("signing_key_enc tidak terbuka dengan kunci %q: %v", master, err)
	}
	return key
}

// Signing key device demo: satu sumber (DemoDeviceSigningKey), tersimpan
// terenkripsi dengan PII_ENC_KEY aktif, seed ulang dengan kunci lain
// menulis ulang ciphertext, kunci sama = no-op.
func TestSeed_PosDeviceSigningKeyEncrypted(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	opts := options(quietLog())

	res, err := seed.Run(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.SigningKeysUpdated != 1 {
		t.Fatalf("seed pertama harus menulis signing key, dapat %d", res.SigningKeysUpdated)
	}
	enc := demoSigningKeyEnc(t, db)
	if enc == nil || bytes.Contains(enc, seed.DemoDeviceSigningKey()) {
		t.Fatal("signing_key_enc kosong atau memuat key polos")
	}
	// Kosong = kunci demo, sama dengan fallback router di luar production.
	if got := openSigningKey(t, config.DemoPIIEncKey, enc); !bytes.Equal(got, seed.DemoDeviceSigningKey()) {
		t.Fatal("isi signing key bukan DemoDeviceSigningKey")
	}
	if len(seed.DemoDeviceSigningKey()) != 32 || seed.DemoDeviceSigningKeyHex() != hex.EncodeToString(seed.DemoDeviceSigningKey()) {
		t.Fatal("signing key demo harus 32 byte, hex huruf kecil")
	}

	// Kunci sama: no-op (ciphertext tidak diganti).
	res, err = seed.Run(ctx, db, opts)
	if err != nil || res.SigningKeysUpdated != 0 || !bytes.Equal(demoSigningKeyEnc(t, db), enc) {
		t.Fatalf("seed ulang kunci sama harus no-op: err=%v updated=%d", err, res.SigningKeysUpdated)
	}

	// PII_ENC_KEY diganti: ciphertext ditulis ulang supaya cocok dengan kunci aktif.
	const otherKey = "kunci-pii-lain-untuk-test-minimal-32-byte"
	opts.PIIEncKey = []byte(otherKey)
	res, err = seed.Run(ctx, db, opts)
	if err != nil || res.SigningKeysUpdated != 1 || res.Total() != 0 {
		t.Fatalf("seed ulang kunci lain: err=%v updated=%d baru=%d", err, res.SigningKeysUpdated, res.Total())
	}
	if got := openSigningKey(t, otherKey, demoSigningKeyEnc(t, db)); !bytes.Equal(got, seed.DemoDeviceSigningKey()) {
		t.Fatal("signing key setelah ganti kunci salah")
	}
}

// Signing key tidak pernah muncul di log seed (polos maupun hex), tetapi
// dicetak `api seed --print-demo`.
func TestSeed_SigningKeyNotLoggedButPrinted(t *testing.T) {
	db := testutil.NewDB(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := seed.Run(context.Background(), db, options(log)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{seed.DemoDeviceSigningKeyHex(), string(seed.DemoDeviceSigningKey())} {
		if strings.Contains(buf.String(), s) {
			t.Fatal("log seed memuat signing key device")
		}
	}
	var out bytes.Buffer
	if err := seed.PrintDemo(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), seed.DemoDeviceSigningKeyHex()) {
		t.Fatal("print-demo tidak memuat signing key device")
	}
}
