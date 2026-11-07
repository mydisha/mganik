package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Batas isi satu transaksi POS (validasi boundary). Semua nilai uang tetap
// di bawah 2^53-1 supaya klien JS menghitung total tanpa kehilangan presisi.
const (
	PosMaxLines        = 200
	PosMaxLineQty      = 1000
	PosMaxUnitPrice    = 100_000_000_000 // Rp100 miliar per unit
	PosMaxPayments     = 10
	PosMaxPaymentValue = 100_000_000_000_000
	PosMaxNoteRunes    = 500
	PosMaxSerialLen    = 64
	PosMaxApprovalLen  = 64
	// posMissingWindow dan posMissingLimit membatasi pencarian gap device_seq.
	posMissingWindow = 10_000
	posMissingLimit  = 500

	posMethodCash = "CASH"
	posMethodEDC  = "EDC"
	posMethodQRIS = "QRIS"
	// posCashProvider: pembayaran tunai dicatat dengan provider_ref turunan
	// id transaksi server (unik per transaksi).
	posCashProvider = "POS_CASH"
)

// ------------------------------------------------------------------ input

// PosSyncInput adalah batch dari device. Transaksi dibiarkan mentah supaya
// signature dihitung atas objek persis seperti yang dikirim.
type PosSyncInput struct {
	ClientTime   *time.Time
	Transactions []json.RawMessage
}

// PosTxnResult adalah hasil per transaksi.
type PosTxnResult struct {
	// Index: posisi transaksi di request (hasil diurutkan menurut device_seq).
	Index       int
	ClientTxnID uuid.UUID
	DeviceSeq   int64
	Status      string
	Result      string
	Detail      json.RawMessage
}

// PosSyncOutput adalah respons batch.
type PosSyncOutput struct {
	ServerTime           time.Time
	AckUpToSeq           int64
	MissingSeqs          []int64
	MissingSeqsTruncated bool
	Results              []PosTxnResult
}

// posTxnPayload adalah isi transaksi yang sudah di-decode ketat.
type posTxnPayload struct {
	ClientTxnID      uuid.UUID       `json:"client_txn_id"`
	DeviceSeq        int64           `json:"device_seq"`
	CashierID        uuid.UUID       `json:"cashier_id"`
	OccurredAt       time.Time       `json:"occurred_at"`
	PriceListVersion int64           `json:"price_list_version"`
	Total            int64           `json:"total"`
	Lines            []posTxnLine    `json:"lines"`
	Payments         []posTxnPayment `json:"payments"`
	Note             string          `json:"note"`
	Signature        string          `json:"signature"`
}

type posTxnLine struct {
	ProductID uuid.UUID `json:"product_id"`
	Qty       int64     `json:"qty"`
	UnitPrice int64     `json:"unit_price"`
	Discount  int64     `json:"discount"`
	Serials   []string  `json:"serials"`
}

type posTxnPayment struct {
	Method      string `json:"method"`
	Amount      int64  `json:"amount"`
	ApprovalRef string `json:"approval_ref"`
}

type posTotals struct {
	Subtotal, Discount, Total, Paid, Cash, NonCash, Change int64
}

// posRejection adalah penolakan yang diputuskan sebelum efek apa pun.
type posRejection struct {
	Reason  string
	Message string
	Errors  []apperror.FieldError
}

// posItem adalah satu transaksi batch beserta hasil pra-proses murni (tanpa DB).
type posItem struct {
	index       int
	raw         json.RawMessage
	clientTxnID uuid.UUID
	seq         int64
	hash        []byte
	payload     *posTxnPayload
	totals      posTotals
	signatureOK bool
	// identErr: client_txn_id/device_seq tidak bisa dibaca. Transaksi tidak
	// bisa disimpan idempoten; dijawab REJECTED invalid_payload tanpa disimpan.
	identErr *posRejection
	// reject: penolakan sebelum efek apa pun. Hanya disimpan kalau signature sah.
	reject *posRejection
}

// posBatch adalah konteks bersama satu request sync.
type posBatch struct {
	deviceID, locationID uuid.UUID
	now                  time.Time
	skewMs               *int64
	cashiers             map[uuid.UUID]repository.PosCashierState // hanya id yang ada di users
	products             map[uuid.UUID]bool
	priceLists           []repository.PosPriceListRow
	priceItems           map[uuid.UUID]map[uuid.UUID]int64 // price list → produk → harga
}

// ------------------------------------------------------------------ sync

// Sync memproses batch transaksi offline (PLANNING §4.3, §7.4, §8.4):
//
//   - Diurutkan menurut device_seq; setiap transaksi diproses di transaksi
//     DB sendiri, jadi satu yang gagal tidak menghentikan yang lain.
//   - Signature HMAC-SHA256 atas JSON kanonis (domain.CanonicalPosJSON)
//     dengan signing key device dicek SEBELUM apa pun disimpan. Transaksi
//     bersignature salah (atau tidak bisa dikanonisasi) TIDAK disimpan dan
//     tidak menempati (device, client_txn_id) maupun (device, device_seq):
//     pemegang token device tanpa signing key tidak bisa "menduduki" seq
//     berikutnya dan membuat penjualan asli ditolak. Signature salah dicatat
//     sebagai anomali POS_PAYLOAD_MISMATCH.
//   - Insert-first idempotency (hanya baris bersignature sah): INSERT
//     pos_transactions ON CONFLICT DO NOTHING. Sudah ada dengan payload_hash
//     sama → DUPLICATE dengan result_detail asli (teks jsonb yang sama
//     persis); hash beda → REJECTED idempotency_key_reused + anomali, dan seq
//     baru itu diisi baris tombstone REJECTED supaya ack_up_to_seq tetap maju.
//     Pengiriman bersamaan tertahan di unique index sampai yang pertama commit.
//   - Penjualan adalah fakta: tidak pernah ditolak karena stok atau serial.
//     Stok lewat RecordFactSales (preempt reservasi online / selisih stok),
//     serial bermasalah jadi konflik/anomali, serial sah tetap dijual.
//   - Materialisasi order channel POS status COMPLETED + line + pembayaran.
func (s *PosService) Sync(ctx context.Context, p *auth.Principal, in PosSyncInput) (PosSyncOutput, error) {
	deviceID, locID, err := posDevice(p)
	if err != nil {
		return PosSyncOutput{}, err
	}
	if in.Transactions == nil {
		return PosSyncOutput{}, apperror.Validation(apperror.FieldError{
			Field: "transactions", Code: "required", Message: "wajib diisi (boleh array kosong)"})
	}
	if len(in.Transactions) > s.cfg.SyncMaxBatch {
		e := apperror.Wrap(ErrPosBatchTooLarge, ErrPosBatchTooLarge.Status, ErrPosBatchTooLarge.Code,
			ErrPosBatchTooLarge.Title, ErrPosBatchTooLarge.Detail)
		e.Fields = []apperror.FieldError{{Field: "transactions", Code: "max",
			Message: fmt.Sprintf("maksimal %d transaksi per batch", s.cfg.SyncMaxBatch)}}
		return PosSyncOutput{}, e
	}
	items := posIdentify(in.Transactions)

	b := &posBatch{deviceID: deviceID, locationID: locID, now: s.clock.Now()}
	if in.ClientTime != nil {
		skew := b.now.Sub(*in.ClientTime).Milliseconds()
		b.skewMs = &skew
	}
	db := s.db.WithContext(ctx)
	dev, err := s.repo.DeviceByID(db, deviceID, false)
	if err != nil {
		return PosSyncOutput{}, err
	}
	if dev == nil || dev.Status != "ACTIVE" {
		return PosSyncOutput{}, ErrInvalidToken
	}
	if dev.SigningKeyEnc == nil {
		return PosSyncOutput{}, ErrPosSigningKeyMissing
	}
	key, err := s.keys.Open(dev.SigningKeyEnc, deviceID[:])
	if err != nil {
		return PosSyncOutput{}, posInvalidSigningKey(err)
	}
	signer := domain.NewPosSigner(key)
	for i := range items {
		if items[i].identErr == nil {
			posPrepare(&items[i], signer, s.cfg.MaxSerialsPerTxn)
		}
	}
	if err := s.prefetch(db, b, items); err != nil {
		return PosSyncOutput{}, err
	}

	// Stabil: id/seq ganda di dalam batch diproses sesuai urutan kirim, jadi
	// salinan kedua menjadi DUPLICATE / device_seq_reused lewat database.
	slices.SortStableFunc(items, func(a, c posItem) int {
		switch {
		case a.seq < c.seq:
			return -1
		case a.seq > c.seq:
			return 1
		}
		return 0
	})
	out := PosSyncOutput{Results: make([]PosTxnResult, 0, len(items))}
	for i := range items {
		if err := ctx.Err(); err != nil {
			return PosSyncOutput{}, err
		}
		out.Results = append(out.Results, s.process(ctx, b, &items[i]))
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		out.ServerTime = s.clock.Now()
		if err := s.repo.TouchDevice(tx, deviceID, out.ServerTime); err != nil {
			return err
		}
		var err error
		if out.AckUpToSeq, err = s.repo.AdvanceAck(tx, deviceID); err != nil {
			return err
		}
		out.MissingSeqs, out.MissingSeqsTruncated, err = s.repo.MissingSeqs(tx, deviceID, out.AckUpToSeq, posMissingWindow, posMissingLimit)
		return err
	})
	if err != nil {
		return PosSyncOutput{}, err
	}
	return out, nil
}

// posIdentify membaca client_txn_id dan device_seq setiap transaksi. Item
// yang identitasnya tidak bisa dibaca tidak menolak seluruh batch: item itu
// dijawab REJECTED invalid_payload (tanpa disimpan, karena tidak punya kunci
// idempotensi) dan sisanya tetap diproses. id/seq ganda di dalam batch
// ditangani database seperti pengiriman ulang.
func posIdentify(raws []json.RawMessage) []posItem {
	items := make([]posItem, len(raws))
	for i, raw := range raws {
		items[i].index, items[i].raw = i, raw
		var fields []apperror.FieldError
		var ident struct {
			ClientTxnID *string      `json:"client_txn_id"`
			DeviceSeq   *json.Number `json:"device_seq"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&ident); err != nil {
			fields = append(fields, apperror.FieldError{Field: "transaction", Code: "invalid_type",
				Message: "transaksi harus objek JSON dengan client_txn_id string dan device_seq angka"})
		} else {
			if ident.ClientTxnID == nil {
				fields = append(fields, apperror.FieldError{Field: "client_txn_id", Code: "required", Message: "wajib diisi"})
			} else if id, err := uuid.Parse(*ident.ClientTxnID); err != nil || id == uuid.Nil {
				fields = append(fields, apperror.FieldError{Field: "client_txn_id", Code: "uuid", Message: "harus UUID yang valid"})
			} else {
				items[i].clientTxnID = id
			}
			if ident.DeviceSeq == nil {
				fields = append(fields, apperror.FieldError{Field: "device_seq", Code: "required", Message: "wajib diisi"})
			} else if seq, err := strconv.ParseInt(string(*ident.DeviceSeq), 10, 64); err != nil || seq < 1 || seq > posSafeInt {
				fields = append(fields, apperror.FieldError{Field: "device_seq", Code: "range", Message: "harus bilangan bulat 1 sampai 2^53-1"})
			} else {
				items[i].seq = seq
			}
		}
		if len(fields) > 0 {
			items[i].identErr = &posRejection{Reason: PosReasonInvalidPayload,
				Message: "client_txn_id/device_seq tidak valid; transaksi tidak disimpan.", Errors: fields}
		}
	}
	return items
}

// posPrepare: kanonisasi, payload_hash, signature, decode ketat, validasi.
// Murni (tanpa DB). Urutan penolakan: payload tidak bisa dikanonisasi →
// signature → surrogate tunggal / skema → aturan bisnis.
func posPrepare(it *posItem, signer *domain.PosSigner, maxSerials int) {
	canonical, info, err := domain.CanonicalPosJSONInfo(it.raw)
	if err != nil {
		h := sha256.Sum256(it.raw)
		it.hash = h[:]
		it.reject = &posRejection{Reason: PosReasonInvalidPayload, Message: "Transaksi bukan JSON kanonis yang valid (angka harus bilangan bulat, tanpa key ganda)."}
		return
	}
	h := domain.PosPayloadHash(canonical)
	it.hash = h[:]

	var sig struct {
		Signature *string `json:"signature"`
	}
	_ = json.Unmarshal(it.raw, &sig)
	if sig.Signature == nil || !signer.Verify(canonical, *sig.Signature) {
		it.reject = &posRejection{Reason: PosReasonInvalidSignature, Message: "Signature transaksi tidak valid untuk device ini."}
		return
	}
	it.signatureOK = true
	if info.LoneSurrogate {
		// Ditandatangani device sendiri (bukan manipulasi), tetapi teksnya
		// bukan Unicode valid: tolak sebagai payload rusak, tanpa anomali.
		it.reject = &posRejection{Reason: PosReasonInvalidPayload,
			Message: "Teks memuat surrogate UTF-16 tunggal (bukan Unicode valid)."}
		return
	}

	var pl posTxnPayload
	dec := json.NewDecoder(bytes.NewReader(it.raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pl); err != nil {
		it.reject = &posRejection{Reason: PosReasonInvalidPayload, Message: "Isi transaksi tidak sesuai skema (tipe salah atau field tidak dikenal)."}
		return
	}
	it.payload = &pl
	it.totals, it.reject = posValidate(&pl, maxSerials)
}

// posBadText: teks bebas dengan karakter kontrol (aturan sama dengan invCheckText).
func posBadText(v string) bool { return invCheckText("v", v) != nil }

// posValidate memeriksa aturan bisnis transaksi dan menghitung total.
// Serial berformat salah TIDAK ditolak di sini (penjualan fakta); dicatat
// sebagai flag SERIAL_INVALID_FORMAT saat diproses.
func posValidate(pl *posTxnPayload, maxSerials int) (posTotals, *posRejection) {
	var t posTotals
	var fields []apperror.FieldError
	add := func(field, code, msg string) {
		fields = append(fields, apperror.FieldError{Field: field, Code: code, Message: msg})
	}
	if pl.CashierID == uuid.Nil {
		add("cashier_id", "required", "wajib diisi")
	}
	if pl.OccurredAt.IsZero() {
		add("occurred_at", "required", "wajib diisi (RFC 3339)")
	}
	if pl.PriceListVersion < 1 || pl.PriceListVersion > posSafeInt {
		add("price_list_version", "range", "wajib bilangan bulat >= 1")
	}
	if n := len([]rune(pl.Note)); n > PosMaxNoteRunes {
		add("note", "max", fmt.Sprintf("maksimal %d karakter", PosMaxNoteRunes))
	}
	if posBadText(pl.Note) {
		add("note", "invalid_characters", "memuat karakter kontrol yang tidak diizinkan")
	}
	if len(pl.Lines) == 0 || len(pl.Lines) > PosMaxLines {
		add("lines", "length", fmt.Sprintf("wajib berisi 1-%d baris", PosMaxLines))
	}
	seenSerial := map[string]string{}
	serialCount := 0
	for i, l := range pl.Lines {
		f := func(s string) string { return fmt.Sprintf("lines[%d].%s", i, s) }
		if l.ProductID == uuid.Nil {
			add(f("product_id"), "required", "wajib diisi")
		}
		lineOK := true
		if l.Qty < 1 || l.Qty > PosMaxLineQty {
			add(f("qty"), "range", fmt.Sprintf("harus 1-%d", PosMaxLineQty))
			lineOK = false
		}
		if l.UnitPrice < 0 || l.UnitPrice > PosMaxUnitPrice {
			add(f("unit_price"), "range", fmt.Sprintf("harus 0-%d", int64(PosMaxUnitPrice)))
			lineOK = false
		}
		if lineOK {
			gross := l.Qty * l.UnitPrice
			if l.Discount < 0 || l.Discount > gross {
				add(f("discount"), "range", "harus 0 sampai qty × unit_price")
			} else {
				t.Subtotal += gross
				t.Discount += l.Discount
			}
		}
		if int64(len(l.Serials)) > max(l.Qty, 0) {
			add(f("serials"), "max", "jumlah serial melebihi qty")
		}
		serialCount += len(l.Serials)
		for j, code := range l.Serials {
			fs := fmt.Sprintf("lines[%d].serials[%d]", i, j)
			norm, _ := domain.NormalizeSerialCode(code)
			switch {
			case norm == "" || len(code) > PosMaxSerialLen:
				add(fs, "length", fmt.Sprintf("wajib 1-%d karakter", PosMaxSerialLen))
			case posBadText(code) || strings.ContainsAny(code, "\n\t"):
				add(fs, "invalid_characters", "memuat karakter kontrol yang tidak diizinkan")
			default:
				if prev, dup := seenSerial[norm]; dup {
					add(fs, "duplicate", "serial sama dengan "+prev)
				} else {
					seenSerial[norm] = fs
				}
			}
		}
	}
	if serialCount > maxSerials {
		add("lines", "max_serials", fmt.Sprintf("maksimal %d serial per transaksi", maxSerials))
	}
	t.Total = t.Subtotal - t.Discount

	if len(pl.Payments) > PosMaxPayments {
		add("payments", "max", fmt.Sprintf("maksimal %d pembayaran", PosMaxPayments))
	}
	cashCount := 0
	seenRef := map[string]int{}
	for i, pay := range pl.Payments {
		f := func(s string) string { return fmt.Sprintf("payments[%d].%s", i, s) }
		if pay.Amount < 1 || pay.Amount > PosMaxPaymentValue {
			add(f("amount"), "range", fmt.Sprintf("harus 1-%d", int64(PosMaxPaymentValue)))
			continue
		}
		switch pay.Method {
		case posMethodCash:
			cashCount++
			if cashCount > 1 {
				add(f("method"), "duplicate", "maksimal satu pembayaran tunai per transaksi")
			}
			if pay.ApprovalRef != "" {
				add(f("approval_ref"), "forbidden", "tidak dipakai untuk tunai")
			}
			t.Cash += pay.Amount
		case posMethodEDC, posMethodQRIS:
			ref := strings.TrimSpace(pay.ApprovalRef)
			switch {
			case ref == "" || len(pay.ApprovalRef) > PosMaxApprovalLen || ref != pay.ApprovalRef:
				add(f("approval_ref"), "length", fmt.Sprintf("wajib 1-%d karakter tanpa spasi di tepi", PosMaxApprovalLen))
			case posBadText(ref) || strings.ContainsAny(ref, "\n\t"):
				add(f("approval_ref"), "invalid_characters", "memuat karakter kontrol yang tidak diizinkan")
			default:
				k := pay.Method + "\x00" + ref
				if j, dup := seenRef[k]; dup {
					add(f("approval_ref"), "duplicate", fmt.Sprintf("sama dengan payments[%d]", j))
				}
				seenRef[k] = i
			}
			t.NonCash += pay.Amount
		default:
			add(f("method"), "oneof", "harus salah satu dari: CASH EDC QRIS")
		}
	}
	t.Paid = t.Cash + t.NonCash

	if len(fields) > 0 {
		return t, posRejectionFor(fields)
	}
	if pl.Total != t.Total {
		return t, &posRejection{Reason: PosReasonTotalMismatch,
			Message: fmt.Sprintf("Total %d tidak sama dengan Σ(qty × harga − diskon) = %d.", pl.Total, t.Total),
			Errors:  []apperror.FieldError{{Field: "total", Code: "total_mismatch", Message: fmt.Sprintf("harus %d", t.Total)}}}
	}
	if t.Total > posSafeInt {
		return t, &posRejection{Reason: PosReasonValidationFailed, Message: "Total melebihi batas aman.",
			Errors: []apperror.FieldError{{Field: "total", Code: "range", Message: "melebihi 2^53-1"}}}
	}
	if t.Paid < t.Total {
		return t, &posRejection{Reason: PosReasonPaymentInsufficient,
			Message: fmt.Sprintf("Pembayaran %d kurang dari total %d.", t.Paid, t.Total),
			Errors:  []apperror.FieldError{{Field: "payments", Code: "payment_insufficient", Message: fmt.Sprintf("kurang %d", t.Total-t.Paid)}}}
	}
	// Kembalian hanya dari tunai: non-tunai tidak boleh melebihi total.
	if t.NonCash > t.Total {
		return t, &posRejection{Reason: PosReasonValidationFailed,
			Message: "Pembayaran non-tunai melebihi total; kembalian hanya dari tunai.",
			Errors:  []apperror.FieldError{{Field: "payments", Code: "non_cash_overpaid", Message: "non-tunai melebihi total"}}}
	}
	t.Change = t.Paid - t.Total
	return t, nil
}

func posRejectionFor(fields []apperror.FieldError) *posRejection {
	r := &posRejection{Reason: PosReasonValidationFailed, Message: "Isi transaksi tidak valid.", Errors: fields}
	for _, f := range fields {
		if f.Code == "invalid_characters" {
			r.Reason, r.Message = PosReasonInvalidCharacters, "Teks memuat karakter kontrol (misal NUL) yang tidak diizinkan."
			break
		}
	}
	return r
}

// prefetch membaca data referensi batch dalam query tetap (tanpa N+1):
// status kasir, keberadaan produk, dan price list POS lokasi.
func (s *PosService) prefetch(db *gorm.DB, b *posBatch, items []posItem) error {
	var cashierIDs, productIDs []uuid.UUID
	seenC, seenP := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	for _, it := range items {
		if it.payload == nil {
			continue
		}
		if id := it.payload.CashierID; id != uuid.Nil && !seenC[id] {
			seenC[id] = true
			cashierIDs = append(cashierIDs, id)
		}
		for _, l := range it.payload.Lines {
			if l.ProductID != uuid.Nil && !seenP[l.ProductID] {
				seenP[l.ProductID] = true
				productIDs = append(productIDs, l.ProductID)
			}
		}
	}
	var err error
	if b.cashiers, err = s.repo.CashierStatus(db, cashierIDs, b.locationID); err != nil {
		return err
	}
	if b.products, err = s.repo.ProductsExisting(db, productIDs); err != nil {
		return err
	}
	if b.priceLists, err = s.repo.POSPriceLists(db, b.locationID); err != nil {
		return err
	}
	listIDs := make([]uuid.UUID, len(b.priceLists))
	for i, l := range b.priceLists {
		listIDs[i] = l.ID
	}
	b.priceItems, err = s.repo.PriceItemsByList(db, listIDs)
	return err
}

// ------------------------------------------------------------------ per transaksi

// posDetail adalah result_detail yang disimpan dan di-replay apa adanya.
type posDetail struct {
	ServerTxnID uuid.UUID             `json:"server_txn_id"`
	OrderID     *uuid.UUID            `json:"order_id,omitempty"`
	Reason      string                `json:"reason,omitempty"`
	Message     string                `json:"message,omitempty"`
	Errors      []apperror.FieldError `json:"errors,omitempty"`
	ReceivedAt  time.Time             `json:"received_at"`
	OccurredAt  *time.Time            `json:"occurred_at,omitempty"`
	ClockSkewMs *int64                `json:"clock_skew_ms,omitempty"`
	Flags       []string              `json:"flags,omitempty"`
	Conflicts   []posConflict         `json:"conflicts,omitempty"`
	SerialsSold []string              `json:"serials_sold,omitempty"`
	// InvalidSerials: kode yang dipindai tetapi bukan format serial (flag
	// SERIAL_INVALID_FORMAT); tidak dicari di database.
	InvalidSerials []string       `json:"invalid_serials,omitempty"`
	Totals         *posTotalsView `json:"totals,omitempty"`
}

type posTotalsView struct {
	Subtotal  int64 `json:"subtotal"`
	Discount  int64 `json:"discount"`
	Total     int64 `json:"total"`
	Paid      int64 `json:"paid"`
	ChangeDue int64 `json:"change_due"`
}

// posConflict adalah satu konflik di result_detail.conflicts.
type posConflict struct {
	Type          string     `json:"type"`
	ConflictID    *uuid.UUID `json:"conflict_id,omitempty"`
	ProductID     *uuid.UUID `json:"product_id,omitempty"`
	Qty           *int64     `json:"qty,omitempty"`
	OrderID       *uuid.UUID `json:"order_id,omitempty"`
	ReservationID *uuid.UUID `json:"reservation_id,omitempty"`
	Code          string     `json:"code,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	Method        string     `json:"method,omitempty"`
	ApprovalRef   string     `json:"approval_ref,omitempty"`
	AnomalyRule   string     `json:"anomaly_rule,omitempty"`
}

// process menjalankan satu transaksi di transaksi DB sendiri (diulang
// otomatis kalau deadlock/serialization failure). Kegagalan sementara
// menghasilkan RETRY tanpa menyimpan apa pun.
func (s *PosService) process(ctx context.Context, b *posBatch, it *posItem) PosTxnResult {
	var out PosTxnResult
	err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		out, err = s.processTx(ctx, tx, b, it)
		return err
	})
	if err == nil {
		return out
	}
	reason := PosReasonTemporaryFailure
	if errors.Is(err, ErrStockContended) {
		reason = PosReasonStockContended
	} else {
		s.log.ErrorContext(ctx, "sync transaksi POS gagal sementara", "device_id", b.deviceID,
			"device_seq", it.seq, "error", err)
	}
	return PosTxnResult{Index: it.index, ClientTxnID: it.clientTxnID, DeviceSeq: it.seq, Status: PosStatusRetry,
		Detail: json.RawMessage(model.MustJSON(map[string]string{"reason": reason}))}
}

// posUnstored: REJECTED yang TIDAK disimpan (tidak menempati kunci
// idempotensi maupun device_seq).
func posUnstored(res PosTxnResult, r *posRejection) PosTxnResult {
	d := map[string]any{"reason": r.Reason, "message": r.Message}
	if len(r.Errors) > 0 {
		d["errors"] = r.Errors
	}
	res.Status, res.Result, res.Detail = PosStatusRejected, PosStatusRejected, json.RawMessage(model.MustJSON(d))
	return res
}

func (s *PosService) processTx(ctx context.Context, tx *gorm.DB, b *posBatch, it *posItem) (PosTxnResult, error) {
	res := PosTxnResult{Index: it.index, ClientTxnID: it.clientTxnID, DeviceSeq: it.seq}
	if it.identErr != nil {
		return posUnstored(res, it.identErr), nil
	}
	if !it.signatureOK {
		// Tidak disimpan: tanpa signature sah, payload ini bukan bukti dari
		// device. Menyimpannya akan menduduki seq/client_txn_id penjualan asli.
		if it.reject.Reason == PosReasonInvalidSignature {
			if err := s.payloadAnomaly(tx, b, it, "signature", it.reject.Reason); err != nil {
				return res, err
			}
		}
		return posUnstored(res, it.reject), nil
	}

	serverID := invNewID()
	occurred := b.now
	var cashier *uuid.UUID
	if it.payload != nil {
		if !it.payload.OccurredAt.IsZero() {
			occurred = it.payload.OccurredAt
		}
		if _, exists := b.cashiers[it.payload.CashierID]; exists {
			c := it.payload.CashierID
			cashier = &c
		}
	}
	inserted, err := s.repo.InsertTxnIfAbsent(tx, repository.PosTxnInsert{
		ID: serverID, DeviceID: b.deviceID, ClientTxnID: it.clientTxnID, DeviceSeq: it.seq, LocationID: b.locationID,
		CashierID: cashier, OccurredAt: occurred, ReceivedAt: b.now, ClockSkewMs: b.skewMs, PayloadHash: it.hash,
	})
	if err != nil {
		return res, err
	}
	if !inserted {
		return s.existing(tx, b, it, res)
	}

	reject := func(r *posRejection) (PosTxnResult, error) {
		d := posDetail{ServerTxnID: serverID, Reason: r.Reason, Message: r.Message, Errors: r.Errors, ReceivedAt: b.now}
		return s.finish(tx, res, serverID, PosStatusRejected, d, nil)
	}
	if it.reject != nil {
		return reject(it.reject)
	}
	pl := it.payload
	// Kasir: ditolak hanya kalau tidak dikenal atau tidak punya role pos.sync
	// di lokasi ini. Kasir yang dinonaktifkan setelah menjual offline tetap
	// diterima (penjualan fakta) dengan flag CASHIER_INACTIVE + anomali.
	if st, ok := b.cashiers[pl.CashierID]; !ok || !st.HasRole {
		return reject(&posRejection{Reason: PosReasonCashierNotAllowed,
			Message: "Kasir tidak dikenal atau tidak punya izin pos.sync di lokasi device ini.",
			Errors:  []apperror.FieldError{{Field: "cashier_id", Code: "cashier_not_allowed", Message: "tidak diizinkan di lokasi ini"}}})
	}
	var unknown []apperror.FieldError
	for i, l := range pl.Lines {
		if !b.products[l.ProductID] {
			unknown = append(unknown, apperror.FieldError{Field: fmt.Sprintf("lines[%d].product_id", i), Code: "not_found", Message: "produk tidak dikenal"})
		}
	}
	if len(unknown) > 0 {
		return reject(&posRejection{Reason: PosReasonUnknownProduct, Message: "Produk tidak ada di katalog.", Errors: unknown})
	}
	return s.accept(ctx, tx, b, it, serverID, res)
}

// posTombstoneNS: namespace UUIDv5 untuk client_txn_id tombstone.
var posTombstoneNS = uuid.MustParse("3a7c1f0e-5b2d-4c8e-9f61-0d4e2b7a9c15")

// posTombstoneID: client_txn_id deterministik untuk baris tombstone satu
// (device, seq), sehingga pengiriman ulang tidak membuat baris kedua.
func posTombstoneID(deviceID uuid.UUID, seq int64) uuid.UUID {
	return uuid.NewSHA1(posTombstoneNS, fmt.Appendf(nil, "%s:seq:%d", deviceID, seq))
}

// existing menangani (device, client_txn_id) atau (device, device_seq) yang
// sudah tersimpan. Hanya dipanggil untuk transaksi bersignature sah, dan
// semua baris tersimpan juga bersignature sah.
func (s *PosService) existing(tx *gorm.DB, b *posBatch, it *posItem, res PosTxnResult) (PosTxnResult, error) {
	stored, err := s.repo.TxnByClientID(tx, b.deviceID, it.clientTxnID)
	if err != nil {
		return res, err
	}
	if stored != nil && bytes.Equal(stored.PayloadHash, it.hash) {
		res.Status, res.Result, res.Detail = PosStatusDuplicate, stored.Result, json.RawMessage(stored.Detail)
		return res, nil
	}
	if stored == nil {
		// Seq sudah terisi transaksi lain: ack tidak terhambat, cukup anomali.
		if err := s.payloadAnomaly(tx, b, it, "seq", PosReasonDeviceSeqReused); err != nil {
			return res, err
		}
		return posUnstored(res, &posRejection{Reason: PosReasonDeviceSeqReused,
			Message: "device_seq sudah dipakai transaksi lain dari device ini."}), nil
	}
	// client_txn_id dipakai ulang dengan isi berbeda. Transaksi asli tetap
	// berlaku. Seq transaksi ini (kalau berbeda dan masih kosong) diisi
	// tombstone REJECTED supaya ack_up_to_seq tidak macet di sini.
	if err := s.payloadAnomaly(tx, b, it, "payload", PosReasonIdempotencyKeyReused); err != nil {
		return res, err
	}
	d := posDetail{Reason: PosReasonIdempotencyKeyReused, ReceivedAt: b.now,
		Message: fmt.Sprintf("client_txn_id sudah dipakai transaksi seq %d dengan isi berbeda (server_txn_id %s).",
			stored.DeviceSeq, stored.ID)}
	if it.seq != stored.DeviceSeq {
		tombID := invNewID()
		inserted, err := s.repo.InsertTxnIfAbsent(tx, repository.PosTxnInsert{
			ID: tombID, DeviceID: b.deviceID, ClientTxnID: posTombstoneID(b.deviceID, it.seq), DeviceSeq: it.seq,
			LocationID: b.locationID, OccurredAt: b.now, ReceivedAt: b.now, ClockSkewMs: b.skewMs, PayloadHash: it.hash,
		})
		if err != nil {
			return res, err
		}
		if inserted {
			d.ServerTxnID = tombID
			return s.finish(tx, res, tombID, PosStatusRejected, d, nil)
		}
	}
	return posUnstored(res, &posRejection{Reason: d.Reason, Message: d.Message}), nil
}

// payloadAnomaly mencatat POS_PAYLOAD_MISMATCH (critical): isi transaksi
// dimanipulasi, client_txn_id/device_seq dipakai ulang, atau signature salah.
// Signature salah di-dedupe per device per hari (hit_count naik, evidence =
// kejadian terakhir) supaya pemegang token tidak bisa membanjiri antrean
// anomali dengan client_txn_id acak.
func (s *PosService) payloadAnomaly(tx *gorm.DB, b *posBatch, it *posItem, kind, reason string) error {
	key := it.clientTxnID.String()
	switch kind {
	case "seq":
		key = strconv.FormatInt(it.seq, 10)
	case "signature":
		key = b.now.Format("2006-01-02")
	}
	dedupe := fmt.Sprintf("POS_PAYLOAD_MISMATCH:%s:%s:%s", kind, b.deviceID, key)
	evidence := model.MustJSON(map[string]any{
		"device_id": b.deviceID, "location_id": b.locationID, "client_txn_id": it.clientTxnID,
		"device_seq": it.seq, "reason": reason, "payload_hash": fmt.Sprintf("%x", it.hash),
	})
	return s.invRepo.UpsertAnomaly(tx, "POS_PAYLOAD_MISMATCH", "POS_DEVICE", b.deviceID, dedupe, evidence, b.now)
}

// finish menulis hasil akhir. Detail respons adalah teks jsonb yang
// dikembalikan database (identik dengan pembacaan ulang untuk DUPLICATE).
func (s *PosService) finish(tx *gorm.DB, res PosTxnResult, serverID uuid.UUID, result string, d posDetail,
	orderID *uuid.UUID) (PosTxnResult, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return res, err
	}
	text, err := s.repo.FinishTxn(tx, serverID, result, raw, orderID)
	if err != nil {
		return res, err
	}
	res.Status, res.Result, res.Detail = result, result, json.RawMessage(text)
	return res, nil
}

// accept mencatat penjualan fakta: stok, serial, order, pembayaran, konflik.
func (s *PosService) accept(ctx context.Context, tx *gorm.DB, b *posBatch, it *posItem, serverID uuid.UUID,
	res PosTxnResult) (PosTxnResult, error) {
	pl, t, now := it.payload, it.totals, b.now
	tol := s.cfg.ClockSkewTolerance

	// Jam device tidak dipercaya untuk keputusan: occurred_at device disimpan
	// apa adanya di pos_transactions; ledger, order, dan pembayaran memakai
	// waktu terkoreksi skew batch (dibatasi tidak melewati waktu terima).
	occurred := pl.OccurredAt.UTC()
	if b.skewMs != nil {
		occurred = occurred.Add(time.Duration(*b.skewMs) * time.Millisecond)
	}
	var flags []string
	if occurred.After(now.Add(tol)) {
		flags = append(flags, PosFlagFutureTimestamp)
	}
	if occurred.After(now) {
		occurred = now
	}
	if b.skewMs != nil && (time.Duration(*b.skewMs)*time.Millisecond > tol || time.Duration(-*b.skewMs)*time.Millisecond > tol) {
		flags = append(flags, PosFlagClockSkew)
	}
	if now.Sub(occurred) > s.cfg.OfflineLimit {
		flags = append(flags, PosFlagOfflineTooLong)
	}
	eff := posEffectiveList(b.priceLists, occurred)
	if eff == nil || eff.Version != pl.PriceListVersion {
		flags = append(flags, PosFlagPriceStale)
	}
	// Uang (semua tetap diterima karena fakta; flag untuk ditinjau).
	var priceMismatch, highDiscount bool
	for _, l := range pl.Lines {
		if eff != nil {
			if price, ok := b.priceItems[eff.ID][l.ProductID]; !ok || price != l.UnitPrice {
				priceMismatch = true
			}
		}
		if l.Discount*100 > int64(s.cfg.HighDiscountPercent)*l.Qty*l.UnitPrice {
			highDiscount = true
		}
	}
	if priceMismatch {
		flags = append(flags, PosFlagPriceMismatch)
	}
	if highDiscount {
		flags = append(flags, PosFlagHighDiscount)
	}
	if t.Total == 0 {
		flags = append(flags, PosFlagZeroTotal)
	}
	overLimit := t.Total > s.cfg.OfflineValueLimit
	if overLimit {
		flags = append(flags, PosFlagOfflineValueLimit)
	}
	cashierState := b.cashiers[pl.CashierID]
	if !cashierState.Active {
		flags = append(flags, PosFlagCashierInactive)
	}
	var invalidSerials []string
	for _, l := range pl.Lines {
		for _, c := range l.Serials {
			if _, ok := domain.NormalizeSerialCode(c); !ok {
				invalidSerials = append(invalidSerials, c)
			}
		}
	}
	if len(invalidSerials) > 0 {
		flags = append(flags, PosFlagSerialInvalidFormat)
	}
	slices.Sort(flags)
	if err := s.flagAnomalies(tx, b, it, serverID, overLimit, !cashierState.Active); err != nil {
		return res, err
	}

	deviceID := b.deviceID
	actor := LedgerActor{Type: model.ActorDevice, ID: &deviceID}

	// 1. Stok: semua produk dalam satu tx, urutan lock reservasi → saldo.
	facts := make([]FactSaleInput, len(pl.Lines))
	for i, l := range pl.Lines {
		facts[i] = FactSaleInput{
			LocationID: b.locationID, ProductID: l.ProductID, Qty: l.Qty, RefType: "POS_TRANSACTION", RefID: serverID,
			IdempotencyKey: fmt.Sprintf("pos:%s:%s:%d", b.deviceID, it.clientTxnID, i), Actor: actor,
			OccurredAt: occurred, PosTransactionID: &serverID,
		}
	}
	sales, err := s.inv.RecordFactSales(ctx, tx, facts)
	if err != nil {
		return res, err
	}
	var conflicts []posConflict
	for i, r := range sales {
		pid := pl.Lines[i].ProductID
		for _, pr := range r.Preempted {
			conflicts = append(conflicts, posConflict{
				Type: PosConflictPreempt, ConflictID: invPtr(pr.ConflictID), ProductID: invPtr(pid), Qty: invPtr(pr.Qty),
				OrderID: invPtr(pr.OrderID), ReservationID: invPtr(pr.ReservationID),
			})
		}
		if r.DiscrepancyQty > 0 {
			conflicts = append(conflicts, posConflict{
				Type: PosConflictDiscrepancy, ConflictID: r.DiscrepancyConflictID, ProductID: invPtr(pid), Qty: invPtr(r.DiscrepancyQty),
			})
		}
	}

	// 2. Order POS COMPLETED + line + riwayat status awal ([*] → COMPLETED).
	orderID := invNewID()
	ext := b.deviceID.String() + ":" + it.clientTxnID.String()
	meta := map[string]any{
		"pos_transaction_id": serverID, "device_id": b.deviceID, "cashier_id": pl.CashierID,
		"client_txn_id": it.clientTxnID, "device_seq": it.seq, "price_list_version": pl.PriceListVersion,
		"change_due": t.Change,
	}
	if len(flags) > 0 {
		meta["flags"] = flags
	}
	if note := strings.TrimSpace(pl.Note); note != "" {
		meta["note"] = note
	}
	paidAt := occurred
	ok, err := s.orders.InsertOrderIfAbsent(tx, &model.Order{
		ID: orderID, ChannelCode: "POS", ExternalRef: &ext, FulfillmentLocationID: &b.locationID, Status: "COMPLETED",
		Currency: "IDR", Subtotal: t.Subtotal, Discount: t.Discount, Total: t.Total, PlacedAt: occurred, PaidAt: &paidAt,
		Metadata: model.MustJSON(meta), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return res, err
	}
	if !ok {
		return res, fmt.Errorf("order POS %s sudah ada untuk transaksi baru", ext)
	}
	lines := make([]model.OrderLine, len(pl.Lines))
	for i, l := range pl.Lines {
		lines[i] = model.OrderLine{ID: invNewID(), OrderID: orderID, ProductID: l.ProductID, Qty: l.Qty,
			UnitPrice: l.UnitPrice, Discount: l.Discount, CreatedAt: now}
	}
	if err := s.orders.InsertLines(tx, lines); err != nil {
		return res, err
	}
	reason := "Transaksi POS"
	if ok, err := s.orders.InsertHistory(tx, &model.OrderStatusHistory{
		ID: invNewID(), OrderID: orderID, ToStatus: "COMPLETED", Reason: &reason,
		ActorType: model.ActorDevice, ActorID: &deviceID, At: occurred,
	}); err != nil {
		return res, err
	} else if !ok {
		return res, ErrOrderInvalidTransition
	}

	// 3. Serial (setelah stok: urutan lock sama dengan fulfillment order).
	sold, serialConflicts, err := s.sellSerials(ctx, tx, b, it, serverID, lines, posCashierOf(pl), occurred)
	if err != nil {
		return res, err
	}
	conflicts = append(conflicts, serialConflicts...)

	// 4. Pembayaran.
	payConflicts, err := s.recordPayments(tx, b, it, serverID, orderID, occurred)
	if err != nil {
		return res, err
	}
	conflicts = append(conflicts, payConflicts...)

	result := PosStatusAccepted
	if len(conflicts) > 0 {
		result = PosStatusAcceptedWithConflict
	}
	d := posDetail{
		ServerTxnID: serverID, OrderID: &orderID, ReceivedAt: now, OccurredAt: &occurred, ClockSkewMs: b.skewMs,
		Flags: flags, Conflicts: conflicts, SerialsSold: sold, InvalidSerials: invalidSerials,
		Totals: &posTotalsView{Subtotal: t.Subtotal, Discount: t.Discount, Total: t.Total, Paid: t.Paid, ChangeDue: t.Change},
	}
	return s.finish(tx, res, serverID, result, d, &orderID)
}

// flagAnomalies mencatat anomali untuk flag yang perlu ditinjau ops:
// nilai di atas batas offline (per transaksi) dan kasir nonaktif (per kasir
// per lokasi, hit_count naik).
func (s *PosService) flagAnomalies(tx *gorm.DB, b *posBatch, it *posItem, serverID uuid.UUID, overLimit, cashierInactive bool) error {
	pl := it.payload
	if overLimit {
		evidence := model.MustJSON(map[string]any{
			"pos_transaction_id": serverID, "device_id": b.deviceID, "location_id": b.locationID,
			"client_txn_id": it.clientTxnID, "total": it.totals.Total, "limit": s.cfg.OfflineValueLimit, "cashier_id": pl.CashierID,
		})
		if err := s.invRepo.UpsertAnomaly(tx, "POS_OFFLINE_VALUE_LIMIT", "POS_TRANSACTION", serverID,
			"POS_OFFLINE_VALUE_LIMIT:"+serverID.String(), evidence, b.now); err != nil {
			return err
		}
	}
	if cashierInactive {
		evidence := model.MustJSON(map[string]any{
			"cashier_id": pl.CashierID, "location_id": b.locationID, "device_id": b.deviceID,
			"pos_transaction_id": serverID, "client_txn_id": it.clientTxnID, "occurred_at": pl.OccurredAt,
		})
		if err := s.invRepo.UpsertAnomaly(tx, "POS_CASHIER_INACTIVE", "USER", pl.CashierID,
			fmt.Sprintf("POS_CASHIER_INACTIVE:%s:%s", b.locationID, pl.CashierID), evidence, b.now); err != nil {
			return err
		}
	}
	return nil
}

func posCashierOf(pl *posTxnPayload) *uuid.UUID { return invPtr(pl.CashierID) }

// Alasan konflik serial (allocation_conflicts.details.reason, kind
// SERIAL_DOUBLE_SALE). Kind sengaja satu (milik read model Command Center);
// sub-alasan membedakan penanganannya.
const (
	posSerialAlreadySold   = "already_sold"
	posSerialWrongLocation = "wrong_location"
	posSerialNotSellable   = "not_sellable_status"
	posSerialNotUnit       = "not_unit"
	posSerialWrongProduct  = "product_mismatch"
	posSerialSellFailed    = "sell_failed"
)

// sellSerials memvalidasi DULU semua serial di bawah lock (FOR UPDATE urut
// id, sama dengan SellSerials), lalu hanya serial yang sah yang dijual lewat
// SellSerials (all-or-nothing untuk himpunan sah itu). Dengan begitu serial
// bermasalah tidak pernah membatalkan penjualan: tidak terdaftar → anomali
// POS_UNREGISTERED_SERIAL; terdaftar tapi keadaannya bertentangan dengan
// penjualan → allocation_conflicts SERIAL_DOUBLE_SALE (+ sub-alasan). Sebagai
// pengaman, SellSerials tetap dijalankan di SAVEPOINT: kalau tetap gagal,
// serial itu jadi konflik sell_failed dan penjualan tetap tercatat.
func (s *PosService) sellSerials(ctx context.Context, tx *gorm.DB, b *posBatch, it *posItem, serverID uuid.UUID,
	lines []model.OrderLine, cashierID *uuid.UUID, occurred time.Time) ([]string, []posConflict, error) {
	type want struct {
		line int
		code string
	}
	var wants []want
	var codes []string
	for i, l := range it.payload.Lines {
		for _, c := range l.Serials {
			// Normalisasi sama dengan verifikasi/klaim (Crockford: huruf
			// besar, tanpa spasi/strip, I/L→1, O→0). Format salah sudah jadi
			// flag SERIAL_INVALID_FORMAT; tidak dicari dan bukan anomali
			// "barang tidak terdaftar".
			n, ok := domain.NormalizeSerialCode(c)
			if !ok {
				continue
			}
			wants = append(wants, want{line: i, code: n})
			codes = append(codes, n)
		}
	}
	if len(wants) == 0 {
		return nil, nil, nil
	}
	states, err := s.repo.LockSerialsByCodes(tx, codes)
	if err != nil {
		return nil, nil, err
	}
	byCode := make(map[string]repository.PosSerialState, len(states))
	for _, st := range states {
		byCode[st.Code] = st
	}

	type issue struct {
		w      want
		st     *repository.PosSerialState
		reason string
	}
	var issues []issue
	var valid []want
	for _, w := range wants {
		st, found := byCode[w.code]
		product := it.payload.Lines[w.line].ProductID
		switch {
		case !found:
			issues = append(issues, issue{w: w})
		case st.LevelCode != domain.LevelUnit:
			issues = append(issues, issue{w: w, st: &st, reason: posSerialNotUnit})
		case st.ProductID == nil || *st.ProductID != product:
			issues = append(issues, issue{w: w, st: &st, reason: posSerialWrongProduct})
		case st.Status == domain.SerialInStock && st.LocationID != nil && *st.LocationID == b.locationID:
			valid = append(valid, w)
		case st.Status == domain.SerialInStock:
			issues = append(issues, issue{w: w, st: &st, reason: posSerialWrongLocation})
		case st.Status == domain.SerialSold || st.Status == domain.SerialConsumerActivated:
			issues = append(issues, issue{w: w, st: &st, reason: posSerialAlreadySold})
		default:
			issues = append(issues, issue{w: w, st: &st, reason: posSerialNotSellable})
		}
	}

	var sold []string
	if len(valid) > 0 {
		validCodes := make([]string, len(valid))
		for i, v := range valid {
			validCodes[i] = v.code
		}
		deviceID := b.deviceID
		var done []TransitionedSerial
		err := invSavepoint(tx, "pos_sell_serials", func() error {
			var err error
			done, err = s.serials.SellSerials(ctx, tx, validCodes, b.locationID, SaleRef{
				RefType: "POS_TRANSACTION", RefID: serverID, OccurredAt: occurred,
				Actor: SerialActor{Type: model.ActorDevice, DeviceID: &deviceID, UserID: cashierID},
			})
			return err
		})
		var appErr *apperror.Error
		switch {
		case err == nil:
			idByCode := make(map[string]uuid.UUID, len(done))
			for _, d := range done {
				idByCode[d.Code] = d.ID
			}
			lineIDs := make([]uuid.UUID, 0, len(valid))
			serialIDs := make([]uuid.UUID, 0, len(valid))
			for _, v := range valid {
				sold = append(sold, v.code)
				lineIDs = append(lineIDs, lines[v.line].ID)
				serialIDs = append(serialIDs, idByCode[v.code])
			}
			if err := s.repo.InsertLineSerials(tx, lineIDs, serialIDs, occurred); err != nil {
				return nil, nil, err
			}
		case errors.As(err, &appErr):
			s.log.WarnContext(ctx, "SellSerials gagal setelah validasi; serial dicatat sebagai konflik",
				"device_id", b.deviceID, "device_seq", it.seq, "code", appErr.Code)
			for _, v := range valid {
				st := byCode[v.code]
				issues = append(issues, issue{w: v, st: &st, reason: posSerialSellFailed})
			}
		default:
			return nil, nil, err
		}
	}

	var conflicts []posConflict
	for _, is := range issues {
		product := it.payload.Lines[is.w.line].ProductID
		if is.st == nil {
			dedupe := fmt.Sprintf("POS_UNREGISTERED_SERIAL:%s:%s", b.locationID, is.w.code)
			evidence := model.MustJSON(map[string]any{
				"code": is.w.code, "location_id": b.locationID, "device_id": b.deviceID, "product_id": product,
				"pos_transaction_id": serverID, "client_txn_id": it.clientTxnID,
			})
			if err := s.invRepo.UpsertAnomaly(tx, "POS_UNREGISTERED_SERIAL", "LOCATION", b.locationID, dedupe, evidence, b.now); err != nil {
				return nil, nil, err
			}
			conflicts = append(conflicts, posConflict{Type: PosConflictSerialUnknown, ProductID: invPtr(product),
				Code: is.w.code, Reason: "not_registered", AnomalyRule: "POS_UNREGISTERED_SERIAL"})
			continue
		}
		id := invNewID()
		qty := int64(1)
		details := map[string]any{
			"code": is.w.code, "reason": is.reason, "recorded_status": is.st.Status, "line_index": is.w.line,
			"recorded_location_id": is.st.LocationID, "recorded_product_id": is.st.ProductID,
			"fact_ref_type": "POS_TRANSACTION", "fact_ref_id": serverID,
		}
		serialID := is.st.ID
		if err := s.invRepo.InsertConflict(tx, &model.AllocationConflict{
			ID: id, Kind: PosConflictSerial, PosTransactionID: &serverID, LocationID: b.locationID, ProductID: product,
			SerialUnitID: &serialID, Qty: &qty, Status: "OPEN", Details: model.MustJSON(details), CreatedAt: b.now, UpdatedAt: b.now,
		}); err != nil {
			return nil, nil, err
		}
		conflicts = append(conflicts, posConflict{Type: PosConflictSerial, ConflictID: &id, ProductID: invPtr(product),
			Code: is.w.code, Reason: is.reason})
	}
	return sold, conflicts, nil
}

// recordPayments menulis pembayaran order POS. Tunai dicatat bersih
// (dibayar − kembalian). EDC/QRIS memakai approval_ref sebagai provider_ref
// (unik per provider). approval_ref yang sudah dipakai transaksi lain tidak
// menggagalkan penjualan: pembayaran dicatat PENDING (menunggu verifikasi)
// dengan ref turunan, plus anomali POS_PAYMENT_REF_REUSED.
func (s *PosService) recordPayments(tx *gorm.DB, b *posBatch, it *posItem, serverID, orderID uuid.UUID,
	occurred time.Time) ([]posConflict, error) {
	var conflicts []posConflict
	for i, pay := range it.payload.Payments {
		method := pay.Method
		p := model.Payment{
			ID: invNewID(), OrderID: orderID, Method: &method, Amount: pay.Amount, Currency: "IDR", Status: "SUCCEEDED",
			ReceivedAt: occurred, CreatedAt: b.now, UpdatedAt: b.now,
		}
		raw := map[string]any{"pos_transaction_id": serverID, "payment_index": i}
		if method == posMethodCash {
			p.Amount = pay.Amount - it.totals.Change
			if p.Amount <= 0 {
				continue // seluruh uang tunai kembali sebagai kembalian
			}
			p.Provider, p.ProviderRef = posCashProvider, fmt.Sprintf("%s:%d", serverID, i)
			raw["tendered"], raw["change_due"] = pay.Amount, it.totals.Change
		} else {
			p.Provider, p.ProviderRef = method, pay.ApprovalRef
		}
		p.Raw = model.MustJSON(raw)
		ok, err := s.orders.InsertPaymentIfAbsent(tx, &p)
		if err != nil {
			return nil, err
		}
		if ok {
			continue
		}
		prev, err := s.orders.PaymentByProviderRef(tx, p.Provider, p.ProviderRef)
		if err != nil {
			return nil, err
		}
		raw["claimed_approval_ref"] = pay.ApprovalRef
		if prev != nil {
			raw["duplicate_of_payment_id"], raw["duplicate_of_order_id"] = prev.ID, prev.OrderID
		}
		p.ProviderRef = pay.ApprovalRef + "#dup:" + serverID.String()
		p.Status, p.Raw = "PENDING", model.MustJSON(raw)
		if ok, err := s.orders.InsertPaymentIfAbsent(tx, &p); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("pembayaran duplikat %s gagal dicatat", p.ProviderRef)
		}
		dedupe := fmt.Sprintf("POS_PAYMENT_REF_REUSED:%s:%s", method, pay.ApprovalRef)
		evidence := model.MustJSON(map[string]any{
			"method": method, "approval_ref": pay.ApprovalRef, "pos_transaction_id": serverID, "order_id": orderID,
			"device_id": b.deviceID, "location_id": b.locationID, "duplicate_of_payment_id": raw["duplicate_of_payment_id"],
		})
		if err := s.invRepo.UpsertAnomaly(tx, "POS_PAYMENT_REF_REUSED", "POS_TRANSACTION", serverID, dedupe, evidence, b.now); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, posConflict{Type: PosConflictPaymentDuplicate, Method: method,
			ApprovalRef: pay.ApprovalRef, Reason: "approval_ref_reused", AnomalyRule: "POS_PAYMENT_REF_REUSED"})
	}
	return conflicts, nil
}
