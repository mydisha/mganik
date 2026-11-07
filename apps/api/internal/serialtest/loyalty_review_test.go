package serialtest

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

func (f *fx) review(id uuid.UUID, decision string, note string) (model.LoyaltyClaim, error) {
	var n *string
	if note != "" {
		n = &note
	}
	return f.loyalty.Review(f.ctx, staffAll("loyalty.review"), id, decision, n)
}

// Regresi QA #1, persis skenarionya: oknum klaim unit SOLD → PENDING skor 80
// → staff REJECT → oknum klaim ulang dengan cookie baru. Dulu: APPROVED skor 0
// dan pembeli asli already_claimed. Sekarang: oknum 403, pembeli asli bisa
// klaim tetapi wajib review.
func TestClaim_QARepro_ReclaimAfterRejectBlocked(t *testing.T) {
	f := newFx(t)
	f.sell(20, 21)
	f.exec(`UPDATE anomaly_rules SET params = '{"max_claims": 1, "window_hours": 24, "risk_weight": 80}' WHERE code = 'CLAIM_ACCOUNT_VELOCITY'`)
	oknum, buyer := f.newConsumer(), f.newConsumer()

	if _, err := f.claim(oknum, 21, unit(21).PIN, "cookie-1"); err != nil { // klaim pertama oknum (lolos)
		t.Fatal(err)
	}
	first, err := f.claim(oknum, 20, unit(20).PIN, "cookie-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Claim.Status != model.ClaimPending || first.Claim.RiskScore != 80 || !first.Claim.ReviewRequired {
		t.Fatalf("klaim berisiko %+v", first.Claim)
	}
	if _, err := f.review(first.Claim.ID, service.ClaimDecisionReject, "pola panen poin"); err != nil {
		t.Fatal(err)
	}
	if s := f.status(unit(20).Code); s != domain.SerialSold {
		t.Fatalf("status setelah tolak %s, ingin SOLD", s)
	}
	// Oknum mencoba lagi dengan cookie (perangkat) baru, setelah jendela velocity lewat.
	f.clk.Advance(48 * time.Hour)
	_, err = f.claim(oknum, 20, unit(20).PIN, "cookie-baru")
	requireCode(t, err, "claim_previously_rejected")
	if n := f.count(`SELECT count(*) FROM loyalty_claims WHERE serial_unit_id = ? AND status IN ('PENDING','APPROVED')`, f.serialID(unit(20).Code)); n != 0 {
		t.Fatalf("klaim aktif oknum %d", n)
	}
	// Pembeli asli: tidak already_claimed, tetapi tidak pernah auto-approve.
	got, err := f.claim(buyer, 20, unit(20).PIN, "hp-pembeli")
	if err != nil {
		t.Fatalf("pembeli asli: %v", err)
	}
	if got.Claim.Status != model.ClaimPending || !got.Claim.ReviewRequired || got.Claim.RiskScore != 0 {
		t.Fatalf("klaim pembeli %+v", got.Claim)
	}
	if n, _ := f.loyalty.ReleaseDueClaims(f.ctx); n != 0 {
		t.Fatal("klaim pada unit berriwayat tolak dilepas otomatis")
	}
	if avail, pending := f.balance(oknum); avail != 10 || pending != 0 {
		t.Fatalf("saldo oknum %d/%d (hanya klaim pertama)", avail, pending)
	}
}

// #2: clawback klaim yang sudah auto-APPROVED.
func TestClaim_ReverseApprovedClaim(t *testing.T) {
	f := newFx(t)
	f.sell(22)
	oknum, buyer := f.newConsumer(), f.newConsumer()
	cl, err := f.claim(oknum, 22, unit(22).PIN, "dev-o")
	if err != nil || cl.Claim.Status != model.ClaimApproved {
		t.Fatalf("klaim auto-approve %+v %v", cl.Claim, err)
	}
	// Alasan wajib.
	_, err = f.review(cl.Claim.ID, service.ClaimDecisionReverse, "")
	requireCode(t, err, "validation_failed")
	_, err = f.review(cl.Claim.ID, service.ClaimDecisionReverse, "   ")
	requireCode(t, err, "validation_failed")

	note := "Struk asli milik pelanggan lain — clawback"
	rev, err := f.review(cl.Claim.ID, service.ClaimDecisionReverse, note)
	if err != nil || rev.Status != model.ClaimReversed {
		t.Fatalf("reverse %+v %v", rev, err)
	}
	if s := f.status(unit(22).Code); s != domain.SerialSold {
		t.Fatalf("status setelah reverse %s", s)
	}
	if n := f.count(`SELECT count(*) FROM audit_logs WHERE action = 'loyalty.claim.review' AND entity_id = ? AND reason = ?
		AND after->>'status' = 'REVERSED'`, cl.Claim.ID, note); n != 1 {
		t.Fatal("audit reverse")
	}
	// REVERSE kedua: idempotent, ledger tidak ganda. Keputusan lain: 409.
	if again, err := f.review(cl.Claim.ID, service.ClaimDecisionReverse, note); err != nil || again.Status != model.ClaimReversed {
		t.Fatalf("reverse ulang %+v %v", again, err)
	}
	_, err = f.review(cl.Claim.ID, service.ClaimDecisionReject, "")
	requireCode(t, err, "claim_already_decided")
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE ref_id = ? AND entry_type = 'REVERSE' AND points = -10`, cl.Claim.ID); n != 1 {
		t.Fatalf("ledger REVERSE %d, ingin 1", n)
	}
	if avail, pending := f.balance(oknum); avail != 0 || pending != 0 {
		t.Fatalf("saldo oknum %d/%d", avail, pending)
	}
	// REVERSE hanya untuk APPROVED.
	f.sell(23)
	f.exec(`UPDATE anomaly_rules SET params = '{"max_claims": 1, "window_hours": 24, "risk_weight": 90}' WHERE code = 'CLAIM_ACCOUNT_VELOCITY'`)
	pend, err := f.claim(oknum, 23, unit(23).PIN, "dev-o")
	if err != nil || pend.Claim.Status != model.ClaimPending {
		t.Fatalf("klaim pending %+v %v", pend.Claim, err)
	}
	_, err = f.review(pend.Claim.ID, service.ClaimDecisionReverse, "coba")
	requireCode(t, err, "claim_already_decided")

	// Pembeli asli berhasil klaim unit yang di-clawback.
	f.exec(`UPDATE anomaly_rules SET params = '{"max_claims": 5, "window_hours": 24, "risk_weight": 40}' WHERE code = 'CLAIM_ACCOUNT_VELOCITY'`)
	got, err := f.claim(buyer, 22, unit(22).PIN, "dev-b")
	if err != nil || got.Claim.Status != model.ClaimPending || !got.Claim.ReviewRequired {
		t.Fatalf("pembeli asli %+v %v", got.Claim, err)
	}
	if ok, err := f.review(got.Claim.ID, service.ClaimDecisionApprove, ""); err != nil || ok.Status != model.ClaimApproved {
		t.Fatalf("approve pembeli %+v %v", ok, err)
	}
	if avail, _ := f.balance(buyer); avail != 10 {
		t.Fatalf("saldo pembeli %d", avail)
	}
}

// #2: keputusan bersaing untuk klaim yang sama → tepat satu menang, ledger
// REVERSE paling banyak satu.
func TestClaim_ConcurrentReviewDecisions(t *testing.T) {
	f := newFx(t)
	f.sell(24, 25)
	f.exec(`UPDATE anomaly_rules SET params = '{"max_claims": 1, "window_hours": 24, "risk_weight": 90}' WHERE code = 'CLAIM_ACCOUNT_VELOCITY'`)
	c := f.newConsumer()
	approved, err := f.claim(c, 24, unit(24).PIN, "d")
	if err != nil || approved.Claim.Status != model.ClaimApproved {
		t.Fatalf("%+v %v", approved.Claim, err)
	}
	pending, err := f.claim(c, 25, unit(25).PIN, "d")
	if err != nil || pending.Claim.Status != model.ClaimPending {
		t.Fatalf("%+v %v", pending.Claim, err)
	}

	race := func(id uuid.UUID, decisions []string) map[string]int {
		var wg sync.WaitGroup
		var mu sync.Mutex
		out := map[string]int{}
		start := make(chan struct{})
		for _, d := range decisions {
			wg.Add(1)
			go func(d string) {
				defer wg.Done()
				<-start
				_, err := f.review(id, d, "keputusan paralel")
				mu.Lock()
				out[d+":"+errCode(err)]++
				mu.Unlock()
			}(d)
		}
		close(start)
		wg.Wait()
		return out
	}

	// Klaim APPROVED: REVERSE x5 bersaing dengan REJECT x5.
	var ds []string
	for range 5 {
		ds = append(ds, service.ClaimDecisionReverse, service.ClaimDecisionReject)
	}
	got := race(approved.Claim.ID, ds)
	if got["REVERSE:"] != 5 || got["REJECT:claim_already_decided"] != 5 {
		t.Fatalf("hasil %v", got)
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE ref_id = ? AND entry_type = 'REVERSE'`, approved.Claim.ID); n != 1 {
		t.Fatalf("ledger REVERSE %d", n)
	}

	// Klaim PENDING: APPROVE x5 bersaing dengan REJECT x5 → satu jenis menang.
	ds = ds[:0]
	for range 5 {
		ds = append(ds, service.ClaimDecisionApprove, service.ClaimDecisionReject)
	}
	got = race(pending.Claim.ID, ds)
	approveWon := got["APPROVE:"] == 5 && got["REJECT:claim_already_decided"] == 5
	rejectWon := got["REJECT:"] == 5 && got["APPROVE:claim_already_decided"] == 5
	if approveWon == rejectWon {
		t.Fatalf("hasil %v", got)
	}
	wantLedger := int64(0)
	if rejectWon {
		wantLedger = 1
	}
	if n := f.count(`SELECT count(*) FROM loyalty_ledger WHERE ref_id = ? AND entry_type = 'REVERSE'`, pending.Claim.ID); n != wantLedger {
		t.Fatalf("ledger REVERSE %d, ingin %d", n, wantLedger)
	}
}

// #3: unit mitra yang klaimnya ditolak kembali AT_PARTNER, sehingga recall
// batch ikut mengarantinanya.
func TestClaim_RejectRestoresStatusSoRecallCatchesIt(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	c := f.newConsumer()
	cl, err := f.claim(c, 30, unit(30).PIN, "d")
	if err != nil {
		t.Fatal(err)
	}
	if s := f.status(unit(30).Code); s != domain.SerialConsumerActivated {
		t.Fatalf("status %s", s)
	}
	if _, err := f.review(cl.Claim.ID, service.ClaimDecisionReject, ""); err != nil {
		t.Fatal(err)
	}
	if s := f.status(unit(30).Code); s != domain.SerialAtPartner {
		t.Fatalf("status setelah tolak %s, ingin AT_PARTNER", s)
	}
	if _, err := f.serials.RecallBatch(f.ctx, f.admin(), seed.BatchID(), "uji recall"); err != nil {
		t.Fatal(err)
	}
	if s := f.status(unit(30).Code); s != domain.SerialQuarantined {
		t.Fatalf("unit lolos recall: %s", s)
	}
	// Unit mitra lain tanpa klaim juga dikarantina.
	if s := f.status(unit(31).Code); s != domain.SerialQuarantined {
		t.Fatalf("unit mitra lain %s", s)
	}
}

// #4: pelepasan klaim jatuh tempo aman dipanggil dari dua instance bersamaan.
func TestReleaseDueClaims_ConcurrentInstances(t *testing.T) {
	f := newFx(t)
	f.shipPalletTo(locSulsel, nil)
	// Unit dari karton berbeda dan perangkat berbeda: tidak ada aturan risiko
	// yang kena, semua murni menunggu masa tunggu.
	for i, u := range []int{0, 12, 24, 36, 1} {
		cl, err := f.claim(f.newConsumer(), u, unit(u).PIN, "dev-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if cl.Claim.ReviewRequired {
			t.Fatalf("klaim %d butuh review: %+v", u, cl.RiskReasons)
		}
	}
	f.clk.Advance(8 * 24 * time.Hour)
	var wg sync.WaitGroup
	counts := make([]int64, 4)
	for i := range counts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := f.loyalty.ReleaseDueClaims(f.ctx)
			if err != nil {
				t.Error(err)
			}
			counts[i] = n
		}(i)
	}
	wg.Wait()
	var total int64
	for _, n := range counts {
		total += n
	}
	if total != 5 {
		t.Fatalf("total dilepas %d dari %v, ingin 5", total, counts)
	}
}
