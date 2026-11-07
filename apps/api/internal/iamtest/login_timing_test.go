package iamtest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
	"mganik/api/internal/service"
)

// Email tidak dikenal tetap membayar biaya argon2id (hash dummy), jadi waktu
// respons tidak bisa dipakai untuk enumerasi akun. Parameter argon2 dibuat
// cukup berat (8 MiB, t=3) supaya selisihnya jauh di atas noise: tanpa hash
// dummy, email tak dikenal ~1 ms vs password salah puluhan ms.
func TestLogin_UnknownEmailCostsSameAsWrongPassword(t *testing.T) {
	e := newEnv(t)
	heavy := domain.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 3, Parallelism: 1, SaltLen: 16, KeyLen: 32}
	hash, err := domain.HashPassword("password-benar-sekali", heavy)
	if err != nil {
		t.Fatal(err)
	}
	uid := e.createUser("berat@mganik.test", "Berat", "ACTIVE", roleSpec{code: "ADMIN", scope: model.ScopeAll})
	if err := e.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, uid).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := pii.New([]byte(testPIIKey))
	if err != nil {
		t.Fatal(err)
	}
	policy := service.DefaultLoginPolicy()
	policy.PasswordParams = heavy
	policy.EmailFailLimit, policy.IPFailLimit = 1000, 1000
	svc := service.NewAuthService(e.db, repository.NewAuthRepository(e.db), e.clock, keys,
		slog.New(slog.NewTextHandler(io.Discard, nil)), policy)

	measure := func(email string) time.Duration {
		var ds []time.Duration
		for range 5 {
			start := time.Now()
			_, err := svc.Login(context.Background(), service.LoginInput{Email: email, Password: "salah-salah", Meta: service.RequestMeta{IP: "192.0.2.7"}})
			ds = append(ds, time.Since(start))
			if !errors.Is(err, service.ErrInvalidCredentials) {
				t.Fatalf("%s: %v", email, err)
			}
		}
		slices.Sort(ds)
		return ds[len(ds)/2]
	}
	known := measure("berat@mganik.test")
	unknown := measure("tidak.ada@mganik.test")
	t.Logf("median password salah %v, email tak dikenal %v", known, unknown)
	if unknown < known/2 {
		t.Fatalf("email tak dikenal jauh lebih cepat (%v vs %v): hash dummy tidak dijalankan", unknown, known)
	}
}
