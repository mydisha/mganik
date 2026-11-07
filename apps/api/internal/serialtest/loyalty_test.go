package serialtest

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/service"
)

func (f *fx) claim(p *auth.Principal, u int, pin, anon string) (service.ClaimResult, error) {
	return f.loyalty.Claim(f.ctx, p, service.ClaimInput{Code: unit(u).Code, PIN: pin, AnonID: anon, ClientIP: "198.51.100.20"})
}

func (f *fx) sell(units ...int) {
	f.t.Helper()
	codes := make([]string, len(units))
	for i, u := range units {
		codes[i] = unit(u).Code
	}
	if _, err := f.serials.SellSerials(f.ctx, nil, codes, locCentral, service.SaleRef{RefType: "ORDER", RefID: uuid.Must(uuid.NewV7())}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) balance(p *auth.Principal) (int64, int64) {
	f.t.Helper()
	b, err := f.loyalty.Balance(f.ctx, p)
	if err != nil {
		f.t.Fatal(err)
	}
	return b.Available, b.Pending
}

func TestClaim_SoldUnitApprovedImmediately(t *testing.T) {
	f := newFx(t)
	f.sell(0)
	c := f.newConsumer()
	res, err := f.claim(c, 0, unit(0).PIN, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	cl := res.Claim
	if cl.Status != model.ClaimApproved || !cl.AvailableAt.Equal(t0) || cl.Points != 10 || cl.RiskScore != 0 || cl.ReviewRequired {
		t.Fatalf("klaim %+v", cl)
	}
	if s := f.status(unit(0).Code); s != domain.SerialConsumerActivated {
		t.Fatalf("status %s", s)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE consumer_id = ? AND entry_type = 'EARN' AND points = 10
		AND idempotency_key = ?`, c.ID, "claim:"+cl.ID.String()+":earn"); n != 1 {
		t.Fatalf("ledger EARN %d", n)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'consumer_activating' AND from_status = 'SOLD'
		AND to_status = 'CONSUMER_ACTIVATED' AND actor_type = 'CONSUMER' AND ref_id = ?`, cl.ID); n != 1 {
		t.Fatalf("event aktivasi %d", n)
	}
	if n := f.count(`SELECT count(*) FROM scan_events WHERE id = ? AND context = 'LOYALTY_CLAIM' AND consumer_id = ?`, cl.ScanEventID, c.ID); n != 1 {
		t.Fatal("scan klaim tidak tercatat")
	}
	if avail, pending := f.balance(c); avail != 10 || pending != 0 {
		t.Fatalf("saldo %d/%d", avail, pending)
	}
	// Kirim ulang oleh akun yang sama (PIN benar): replay, tanpa poin ganda.
	again, err := f.claim(c, 0, unit(0).PIN, "dev-1")
	if err != nil || !again.Replayed || again.Claim.ID != cl.ID {
		t.Fatalf("replay %+v %v", again, err)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE consumer_id = ?`, c.ID); n != 1 {
		t.Fatalf("ledger setelah replay %d", n)
	}
	// Akun lain dengan PIN yang sama: sudah diklaim.
	_, err = f.claim(f.newConsumer(), 0, unit(0).PIN, "dev-2")
	requireCode(t, err, "already_claimed")
}

// Unit di mitra (penjualan tidak diketahui): PENDING selama masa tunggu, lalu
// dilepas job ReleaseDueClaims (clock.Fake).
func TestClaim_AtPartnerWaitingPeriod(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	c := f.newConsumer()
	res, err := f.claim(c, 3, unit(3).PIN, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Claim.Status != model.ClaimPending || res.Claim.ReviewRequired || !res.Claim.AvailableAt.Equal(t0.Add(7*24*time.Hour)) {
		t.Fatalf("klaim %+v", res.Claim)
	}
	if avail, pending := f.balance(c); avail != 0 || pending != 10 {
		t.Fatalf("saldo sebelum masa tunggu %d/%d", avail, pending)
	}
	f.clk.Advance(7*24*time.Hour - time.Minute)
	if n, err := f.loyalty.ReleaseDueClaims(f.ctx); err != nil || n != 0 {
		t.Fatalf("dilepas sebelum waktunya: %d %v", n, err)
	}
	f.clk.Advance(2 * time.Minute)
	if n, err := f.loyalty.ReleaseDueClaims(f.ctx); err != nil || n != 1 {
		t.Fatalf("pelepasan %d %v", n, err)
	}
	if n, _ := f.loyalty.ReleaseDueClaims(f.ctx); n != 0 {
		t.Fatal("pelepasan tidak idempotent")
	}
	if avail, pending := f.balance(c); avail != 10 || pending != 0 {
		t.Fatalf("saldo setelah masa tunggu %d/%d", avail, pending)
	}
}

// Gerbang lifecycle: unit belum terjual ditolak walau kode + PIN benar, dan
// tercatat sebagai anomali (indikasi panen poin orang dalam).
func TestClaim_UnsoldUnitRejectedWithAnomaly(t *testing.T) {
	f := newFx(t)
	c := f.newConsumer()
	_, err := f.claim(c, 5, unit(5).PIN, "dev-1") // IN_STOCK di gudang pusat
	requireCode(t, err, "claim_not_allowed")
	if s := f.status(unit(5).Code); s != domain.SerialInStock {
		t.Fatalf("status berubah %s", s)
	}
	a, ok := f.anomaly(domain.RuleNonSellableScan, "NON_SELLABLE_SCAN:claim:"+f.serialID(unit(5).Code))
	if !ok || f.count(`SELECT count(*) FROM anomalies WHERE id = ? AND evidence->>'context' = 'LOYALTY_CLAIM'`, a.ID) != 1 {
		t.Fatalf("anomali klaim %+v", a)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_claims`); n != 0 {
		t.Fatalf("klaim tersimpan %d", n)
	}
}

func TestClaim_WrongPINLockout(t *testing.T) {
	f := newFx(t)
	f.sell(6)
	c := f.newConsumer()
	for i := 1; i <= 4; i++ {
		_, err := f.claim(c, 6, "0000000000", "dev-1")
		requireCode(t, err, "invalid_code_or_pin")
	}
	_, err := f.claim(c, 6, "0000000000", "dev-1")
	requireCode(t, err, "claim_locked")
	// PIN benar setelah terkunci tetap ditolak.
	_, err = f.claim(c, 6, unit(6).PIN, "dev-1")
	requireCode(t, err, "claim_locked")
	if n := f.count(`SELECT claim_failed_attempts FROM serial_units WHERE code = ? AND claim_locked_at IS NOT NULL`, unit(6).Code); n != 5 {
		t.Fatalf("percobaan %d, ingin 5", n)
	}
	if _, ok := f.anomaly(domain.RuleClaimPINBruteForce, "CLAIM_PIN_BRUTE_FORCE:"+f.serialID(unit(6).Code)); !ok {
		t.Fatal("anomali brute force tidak tercatat")
	}
	// Kode/PIN berformat salah tidak menyentuh unit.
	_, err = f.loyalty.Claim(f.ctx, c, service.ClaimInput{Code: "salah", PIN: unit(7).PIN})
	requireCode(t, err, "invalid_code_or_pin")
	// PIN salah sekali lalu benar: percobaan direset.
	f.sell(7)
	_, err = f.claim(c, 7, "1111111111", "dev-1")
	requireCode(t, err, "invalid_code_or_pin")
	if _, err := f.claim(c, 7, unit(7).PIN, "dev-1"); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT claim_failed_attempts FROM serial_units WHERE code = ?`, unit(7).Code); n != 0 {
		t.Fatalf("percobaan tidak direset: %d", n)
	}
}

// Brute force paralel tidak bisa melewati batas 5: setiap percobaan menunggu
// lock percobaan sebelumnya.
func TestClaim_ParallelBruteForceStopsAtFive(t *testing.T) {
	f := newFx(t)
	f.sell(8)
	const workers = 30
	var wg sync.WaitGroup
	codes := make([]string, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := f.newConsumer()
			<-start
			_, err := f.claim(c, 8, "ZZZZZZZZZZ", "dev-par")
			codes[i] = errCode(err)
		}(i)
	}
	close(start)
	wg.Wait()
	invalid, locked := 0, 0
	for _, c := range codes {
		switch c {
		case "invalid_code_or_pin":
			invalid++
		case "claim_locked":
			locked++
		default:
			t.Fatalf("hasil tak terduga %q", c)
		}
	}
	if invalid != 4 || locked != workers-4 {
		t.Fatalf("invalid %d, locked %d", invalid, locked)
	}
	if n := f.count(`SELECT claim_failed_attempts FROM serial_units WHERE code = ?`, unit(8).Code); n != 5 {
		t.Fatalf("percobaan tercatat %d, ingin tepat 5", n)
	}
	_, err := f.claim(f.newConsumer(), 8, unit(8).PIN, "dev-x")
	requireCode(t, err, "claim_locked")
}

// Dua konsumen mengklaim unit yang sama bersamaan: satu sukses, satu already_claimed.
func TestClaim_ConcurrentDoubleClaim(t *testing.T) {
	f := newFx(t)
	f.sell(9)
	const workers = 8
	var wg sync.WaitGroup
	codes := make([]string, workers)
	consumers := make([]*auth.Principal, workers)
	for i := range consumers {
		consumers[i] = f.newConsumer()
	}
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := f.claim(consumers[i], 9, unit(9).PIN, "dev-"+consumers[i].ID.String())
			codes[i] = errCode(err)
		}(i)
	}
	close(start)
	wg.Wait()
	ok, dup := 0, 0
	for _, c := range codes {
		switch c {
		case "":
			ok++
		case "already_claimed":
			dup++
		default:
			t.Fatalf("hasil tak terduga %q", c)
		}
	}
	if ok != 1 || dup != workers-1 {
		t.Fatalf("sukses %d, duplikat %d", ok, dup)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_claims WHERE status IN ('PENDING','APPROVED')`); n != 1 {
		t.Fatalf("klaim aktif %d", n)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger`); n != 1 {
		t.Fatalf("ledger %d", n)
	}
}

// Klaim oknum ditolak reviewer → poin dibalik, pembeli asli tetap bisa klaim.
func TestClaim_RejectedThenGenuineBuyerClaims(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	oknum, buyer := f.newConsumer(), f.newConsumer()
	first, err := f.claim(oknum, 10, unit(10).PIN, "dev-oknum")
	if err != nil {
		t.Fatal(err)
	}
	note := "Klaim dari kasir — terlihat di CCTV 📹"
	reviewer := staffAll("loyalty.review")
	got, err := f.loyalty.Review(f.ctx, reviewer, first.Claim.ID, service.ClaimDecisionReject, &note)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.ClaimRejected || got.DecidedAt == nil {
		t.Fatalf("review %+v", got)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE consumer_id = ? AND entry_type = 'REVERSE' AND points = -10`, oknum.ID); n != 1 {
		t.Fatalf("ledger REVERSE %d", n)
	}
	if avail, pending := f.balance(oknum); avail != 0 || pending != 0 {
		t.Fatalf("saldo oknum %d/%d", avail, pending)
	}
	if n := f.count(`SELECT count(*) FROM audit_logs WHERE action = 'loyalty.claim.review' AND entity_id = ? AND reason = ?`, first.Claim.ID, note); n != 1 {
		t.Fatal("audit review tidak tercatat dengan catatan unicode")
	}
	// Idempotent: keputusan sama dikirim ulang; keputusan berbeda ditolak.
	if _, err := f.loyalty.Review(f.ctx, reviewer, first.Claim.ID, service.ClaimDecisionReject, nil); err != nil {
		t.Fatal(err)
	}
	_, err = f.loyalty.Review(f.ctx, reviewer, first.Claim.ID, service.ClaimDecisionApprove, nil)
	requireCode(t, err, "claim_already_decided")
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE consumer_id = ? AND entry_type = 'REVERSE'`, oknum.ID); n != 1 {
		t.Fatal("REVERSE ganda")
	}

	// Klaim ditolak → unit kembali ke status asal (AT_PARTNER) lewat transisi
	// claim_reversal, jadi ikut recall/laporan jual mitra lagi.
	if s := f.status(unit(10).Code); s != domain.SerialAtPartner {
		t.Fatalf("status setelah tolak %s, ingin AT_PARTNER", s)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'claim_reversal' AND from_status = 'CONSUMER_ACTIVATED'
		AND to_status = 'AT_PARTNER' AND ref_id = ?`, first.Claim.ID); n != 1 {
		t.Fatal("event claim_reversal tidak tercatat")
	}
	// Pembeli asli: klaim baru selalu menunggu review (unit punya riwayat tolak).
	second, err := f.claim(buyer, 10, unit(10).PIN, "dev-buyer")
	if err != nil {
		t.Fatalf("pembeli asli ditolak: %v", err)
	}
	if second.Claim.Status != model.ClaimPending || !second.Claim.ReviewRequired || second.Claim.ConsumerID != buyer.ID {
		t.Fatalf("klaim pembeli %+v", second.Claim)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'consumer_activating' AND from_status = 'AT_PARTNER' AND ref_id = ?`, second.Claim.ID); n != 1 {
		t.Fatal("aktivasi kedua tidak tercatat dari AT_PARTNER")
	}
	_, err = f.loyalty.Review(f.ctx, reviewer, uuid.Must(uuid.NewV7()), service.ClaimDecisionApprove, nil)
	requireCode(t, err, "claim_not_found")
}

// Skor risiko dari aturan CLAIM_* (bobot di params) di-clamp ke 100 walau
// total bobot di atas 100; skor ≥ ambang → PENDING review.
func TestClaim_RiskScoreClampedAndReview(t *testing.T) {
	f := newFx(t)
	f.sell(11, 12, 13)
	f.exec(`UPDATE anomaly_rules SET params = '{"max_accounts": 1, "risk_weight": 80}' WHERE code = 'CLAIM_SHARED_DEVICE'`)
	f.exec(`UPDATE anomaly_rules SET params = '{"max_claims": 1, "window_hours": 24, "risk_weight": 70}' WHERE code = 'CLAIM_ACCOUNT_VELOCITY'`)
	f.exec(`UPDATE anomaly_rules SET params = '{"radius_m": 100, "during_opening_hours": true, "risk_weight": 30}' WHERE code = 'CLAIM_NEAR_STORE'`)

	a, b := f.newConsumer(), f.newConsumer()
	if _, err := f.claim(a, 11, unit(11).PIN, "dev-bersama"); err != nil {
		t.Fatal(err)
	}
	// Akun B, perangkat sama (shared device 80); klaim pertama B (velocity tidak kena).
	resB, err := f.claim(b, 12, unit(12).PIN, "dev-bersama")
	if err != nil {
		t.Fatal(err)
	}
	if resB.Claim.RiskScore != 80 || resB.Claim.Status != model.ClaimPending || !resB.Claim.ReviewRequired {
		t.Fatalf("klaim B %+v", resB.Claim)
	}
	// Klaim kedua B dari perangkat sama, di depan toko saat jam buka:
	// 80 + 70 + 30 = 180 → di-clamp 100.
	resB2, err := f.loyalty.Claim(f.ctx, b, service.ClaimInput{
		Code: unit(13).Code, PIN: unit(13).PIN, AnonID: "dev-bersama", ClientIP: "198.51.100.20", GPS: &geoStoreJaks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resB2.Claim.RiskScore != 100 || len(resB2.RiskReasons) != 3 {
		t.Fatalf("skor %d alasan %+v", resB2.Claim.RiskScore, resB2.RiskReasons)
	}
	if n := f.count(`SELECT risk_score FROM loyalty_claims WHERE id = ?`, resB2.Claim.ID); n != 100 {
		t.Fatalf("risk_score DB %d", n)
	}
	if n := f.count(`SELECT count(*) FROM anomalies WHERE rule_code IN ('CLAIM_SHARED_DEVICE','CLAIM_ACCOUNT_VELOCITY','CLAIM_NEAR_STORE')`); n != 3 {
		t.Fatalf("anomali risiko %d", n)
	}
	// Klaim review_required tidak dilepas otomatis walau unitnya SOLD.
	f.clk.Advance(30 * 24 * time.Hour)
	if n, _ := f.loyalty.ReleaseDueClaims(f.ctx); n != 0 {
		t.Fatalf("klaim berisiko dilepas otomatis: %d", n)
	}
	ok, err := f.loyalty.Review(f.ctx, staffAll("loyalty.review"), resB2.Claim.ID, service.ClaimDecisionApprove, nil)
	if err != nil || ok.Status != model.ClaimApproved {
		t.Fatalf("approve %+v %v", ok, err)
	}
}

func TestClaim_InactiveConsumerAndWrongKind(t *testing.T) {
	f := newFx(t)
	f.sell(14)
	c := f.newConsumer()
	f.exec(`UPDATE consumers SET status = 'BLOCKED' WHERE id = ?`, c.ID)
	_, err := f.claim(c, 14, unit(14).PIN, "dev")
	requireCode(t, err, "consumer_inactive")
	_, err = f.claim(staffAll("loyalty.review"), 14, unit(14).PIN, "dev")
	requireCode(t, err, "forbidden")
}
