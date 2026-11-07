package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/pagination"
	"mganik/api/internal/pii"
	"mganik/api/internal/repository"
)

const (
	// posTokenBytes dan posSigningKeyBytes: 256 bit acak dari crypto/rand.
	posTokenBytes      = 32
	posSigningKeyBytes = 32
	// posTokenPrefix memudahkan secret scanning mengenali token device.
	posTokenPrefix = "pos_"
	// PosSerialPageSize: serial per halaman bootstrap (keyset per kode).
	// 2.000 baris ≈ 140 KB JSON; toko dengan stok serial lebih besar
	// mengambil halaman berikutnya lewat GET /pos/bootstrap/serials.
	PosSerialPageSize = 2000
	// posMaxDeviceName dan posMaxReason: batas teks bebas (rune).
	posMaxDeviceName = 100
	posMaxReason     = 500
	// posStaleBatch: maksimum lokasi yang ditandai OFFLINE per putaran job.
	posStaleBatch = 200

	cursorPosDevices = "pos.devices.v1"
	cursorPosSerials = "pos.serials.v1"
)

// PosService: provisioning device, bootstrap data offline, heartbeat, sync
// transaksi POS, dan resolusi konflik alokasi (Fase 4).
type PosService struct {
	db      *gorm.DB
	clock   clock.Clock
	cfg     config.POSConfig
	keys    *pii.SigningKeyCipher
	inv     *InventoryService
	serials *SerialService
	log     *slog.Logger
	rand    io.Reader

	repo    repository.PosRepository
	orders  repository.OrderRepository
	invRepo repository.InventoryRepository
}

// NewPosService merangkai service. cfg nol = default (POSConfig.WithDefaults).
func NewPosService(db *gorm.DB, clk clock.Clock, cfg config.POSConfig, keys *pii.SigningKeyCipher,
	inv *InventoryService, serials *SerialService, log *slog.Logger) *PosService {
	if clk == nil {
		clk = clock.Real{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &PosService{
		db: db, clock: clk, cfg: cfg.WithDefaults(), keys: keys, inv: inv, serials: serials, log: log, rand: rand.Reader,
	}
}

// Config mengembalikan konfigurasi POS efektif (setelah default).
func (s *PosService) Config() config.POSConfig { return s.cfg }

// ------------------------------------------------------------------ otorisasi

// posStaff: staff login dengan permission perm (scope dicek per objek).
func posStaff(p *auth.Principal, perm string) error {
	if p == nil {
		return apperror.ErrUnauthenticated
	}
	if p.Kind != auth.KindStaff || !p.Can(perm) {
		return apperror.ErrForbidden
	}
	return nil
}

// posDevice: principal device POS dengan pos.sync di lokasinya sendiri.
func posDevice(p *auth.Principal) (deviceID, locationID uuid.UUID, err error) {
	if p == nil {
		return uuid.Nil, uuid.Nil, apperror.ErrUnauthenticated
	}
	if p.Kind != auth.KindDevice || len(p.LocationIDs) != 1 || !p.CanAt(PermPosSync, p.LocationIDs[0]) {
		return uuid.Nil, uuid.Nil, apperror.ErrForbidden
	}
	return p.ID, p.LocationIDs[0], nil
}

func posAudit(p *auth.Principal, action, entity string, id uuid.UUID, before, after any, reason *string,
	requestID string, now time.Time) *model.AuditLog {
	actor := LedgerActorFromPrincipal(p)
	a := &model.AuditLog{
		ID: invNewID(), ActorType: actor.Type, ActorID: actor.ID, Action: action, Entity: entity,
		EntityID: &id, Reason: reason, At: now,
	}
	if before != nil {
		a.Before = model.MustJSON(before)
	}
	if after != nil {
		a.After = model.MustJSON(after)
	}
	if requestID != "" {
		a.RequestID = &requestID
	}
	return a
}

// ------------------------------------------------------------------ provisioning

// PosProvisionInput adalah permintaan provisioning device.
type PosProvisionInput struct {
	LocationID uuid.UUID
	Name       string
	RequestID  string
}

// PosProvisioned berisi rahasia yang hanya dikembalikan sekali.
type PosProvisioned struct {
	Device        repository.PosDeviceRow
	Token         string
	SigningKeyHex string
}

// ProvisionDevice membuat device POS baru di toko (permission
// pos.device.manage di lokasi itu). Token bearer dan signing key acak 256 bit
// hanya dikembalikan di sini; DB menyimpan SHA-256 token dan signing key
// terenkripsi AES-GCM (subkey HKDF dari PII_ENC_KEY, AAD = id device).
//
// Sengaja tanpa Idempotency-Key replay: replay berarti menyimpan token polos
// di idempotency_keys. Retry yang membuat device ganda diselesaikan dengan
// mencabut device yang tidak dipakai.
func (s *PosService) ProvisionDevice(ctx context.Context, p *auth.Principal, in PosProvisionInput) (PosProvisioned, error) {
	if err := posStaff(p, PermPosDeviceManage); err != nil {
		return PosProvisioned{}, err
	}
	name, n := invTrimmed(in.Name)
	var fields []apperror.FieldError
	if in.LocationID == uuid.Nil {
		fields = append(fields, apperror.FieldError{Field: "location_id", Code: "required", Message: "wajib diisi"})
	}
	if n < 1 || n > posMaxDeviceName {
		fields = append(fields, apperror.FieldError{Field: "name", Code: "length",
			Message: fmt.Sprintf("wajib 1-%d karakter", posMaxDeviceName)})
	}
	if len(fields) > 0 {
		return PosProvisioned{}, apperror.Validation(fields...)
	}
	if err := invCheckText("name", name); err != nil {
		return PosProvisioned{}, err
	}
	if !p.CanAt(PermPosDeviceManage, in.LocationID) {
		return PosProvisioned{}, apperror.ErrForbidden
	}

	raw := make([]byte, posTokenBytes+posSigningKeyBytes)
	if _, err := io.ReadFull(s.rand, raw); err != nil {
		return PosProvisioned{}, fmt.Errorf("membuat token device: %w", err)
	}
	token := posTokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:posTokenBytes])
	signingKey := raw[posTokenBytes:]
	id := invNewID()
	enc, err := s.keys.Seal(signingKey, id[:])
	if err != nil {
		return PosProvisioned{}, err
	}
	tokenHash := hashDeviceToken(token)

	var out PosProvisioned
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loc, err := s.repo.LocationByID(tx, in.LocationID)
		if err != nil {
			return err
		}
		if loc == nil || loc.ArchivedAt != nil {
			return ErrStockLocationNotFound
		}
		if loc.TypeCode != "OWN_STORE" {
			return ErrPosLocationNotStore
		}
		now := s.clock.Now()
		actor := p.ID
		if err := s.repo.InsertDevice(tx, &model.PosDevice{
			ID: id, LocationID: in.LocationID, Name: name, TokenHash: tokenHash[:], SigningKeyEnc: enc,
			ProvisionedBy: &actor, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := s.repo.MarkLocationTracked(tx, in.LocationID, now); err != nil {
			return err
		}
		// Audit tanpa rahasia apa pun (token/signing key tidak pernah masuk log/audit).
		if err := s.invRepo.InsertAudit(tx, posAudit(p, "pos_device.provisioned", "pos_devices", id, nil,
			map[string]any{"location_id": in.LocationID, "name": name, "status": "ACTIVE"}, nil, in.RequestID, now)); err != nil {
			return err
		}
		d, err := s.repo.DeviceByID(tx, id, false)
		if err != nil {
			return err
		}
		out.Device = *d
		return nil
	})
	if err != nil {
		return PosProvisioned{}, err
	}
	out.Token = token
	out.SigningKeyHex = hex.EncodeToString(signingKey)
	return out, nil
}

// RevokeDevice mencabut device (token langsung ditolak resolver, 401).
// Mencabut device yang sudah dicabut mengembalikan keadaannya (idempotent).
//
// Batasan yang disengaja: penjualan offline yang BELUM tersinkron dari device
// yang sudah dicabut tidak bisa masuk lewat /pos/sync (token ditolak). Itu
// fakta yang hilang dari stok server sampai dipulihkan manual: ekspor outbox
// dari device (atau struk), lalu ops mencatatnya (adjustment/opname) setelah
// diverifikasi. Sebelum mencabut device yang masih dipegang toko, pastikan
// outbox-nya kosong (pending_count = 0 di heartbeat terakhir).
func (s *PosService) RevokeDevice(ctx context.Context, p *auth.Principal, id uuid.UUID, reason, requestID string) (repository.PosDeviceRow, error) {
	if err := posStaff(p, PermPosDeviceManage); err != nil {
		return repository.PosDeviceRow{}, err
	}
	reason, n := invTrimmed(reason)
	if n > posMaxReason {
		return repository.PosDeviceRow{}, apperror.Validation(apperror.FieldError{
			Field: "reason", Code: "max", Message: fmt.Sprintf("maksimal %d karakter", posMaxReason)})
	}
	if err := invCheckText("reason", reason); err != nil {
		return repository.PosDeviceRow{}, err
	}
	var out repository.PosDeviceRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		d, err := s.repo.DeviceByID(tx, id, true)
		if err != nil {
			return err
		}
		if d == nil {
			return ErrPosDeviceNotFound
		}
		if !p.CanAt(PermPosDeviceManage, d.LocationID) {
			return apperror.ErrForbidden
		}
		if d.Status == "REVOKED" {
			out = *d
			return nil
		}
		now := s.clock.Now()
		if _, err := s.repo.RevokeDevice(tx, id, now); err != nil {
			return err
		}
		var why *string
		if reason != "" {
			why = &reason
		}
		if err := s.invRepo.InsertAudit(tx, posAudit(p, "pos_device.revoked", "pos_devices", id,
			map[string]any{"status": d.Status, "version": d.Version},
			map[string]any{"status": "REVOKED", "version": d.Version + 1}, why, requestID, now)); err != nil {
			return err
		}
		fresh, err := s.repo.DeviceByID(tx, id, false)
		if err != nil {
			return err
		}
		out = *fresh
		return nil
	})
	return out, err
}

// PosDeviceQuery adalah filter GET /pos/devices.
type PosDeviceQuery struct {
	LocationID *uuid.UUID
	Limit      string
	Cursor     string
}

// ListDevices menampilkan device di lokasi tempat principal punya
// pos.device.manage (scope per permission, bukan gabungan).
func (s *PosService) ListDevices(ctx context.Context, p *auth.Principal, q PosDeviceQuery) (pagination.Page[repository.PosDeviceRow], error) {
	var zero pagination.Page[repository.PosDeviceRow]
	if err := posStaff(p, PermPosDeviceManage); err != nil {
		return zero, err
	}
	if q.LocationID != nil && !p.CanAt(PermPosDeviceManage, *q.LocationID) {
		return zero, apperror.ErrForbidden
	}
	var key pagination.TimeID
	req, err := pagination.Parse(q.Limit, q.Cursor, cursorPosDevices, &key)
	if err != nil {
		return zero, err
	}
	scope := p.PermissionScopes[PermPosDeviceManage]
	f := repository.PosDeviceFilter{
		AllLocations: scope.All, LocationIDs: scope.LocationIDs, LocationID: q.LocationID, Limit: req.Limit,
	}
	if req.HasCursor {
		f.AfterAt, f.AfterID = &key.At, &key.ID
	}
	rows, err := s.repo.ListDevices(s.db.WithContext(ctx), f)
	if err != nil {
		return zero, err
	}
	return pagination.Trim(rows, req.Limit, cursorPosDevices, func(r repository.PosDeviceRow) any {
		return pagination.TimeID{At: r.CreatedAt, ID: r.ID}
	})
}

// ------------------------------------------------------------------ bootstrap

// PosEffectivePriceList adalah daftar harga POS yang berlaku beserta isinya.
type PosEffectivePriceList struct {
	repository.PosPriceListRow
	Items []repository.PosPriceRow
}

// PosBootstrap adalah data referensi untuk operasi offline.
type PosBootstrap struct {
	Now       time.Time
	Device    repository.PosDeviceRow
	Location  repository.PosLocationRow
	Products  []repository.PosProductRow
	PriceList *PosEffectivePriceList
	Stock     []repository.PosStockRow
	Cashiers  []repository.PosCashierRow
	Serials   pagination.Page[repository.PosSerialRow]
}

// posEffectiveList memilih price list POS yang berlaku pada waktu at: list
// khusus lokasi (versi tertinggi) menggantikan list default seluruhnya;
// tanpa list khusus lokasi, list default versi tertinggi. price_list_version
// yang dibawa device adalah versi list terpilih ini.
func posEffectiveList(lists []repository.PosPriceListRow, at time.Time) *repository.PosPriceListRow {
	var best *repository.PosPriceListRow
	for i := range lists {
		l := &lists[i]
		if at.Before(l.ValidFrom) || (l.ValidTo != nil && !at.Before(*l.ValidTo)) {
			continue
		}
		switch {
		case best == nil:
			best = l
		case l.LocationID != nil && best.LocationID == nil:
			best = l
		case (l.LocationID == nil) == (best.LocationID == nil) && l.Version > best.Version:
			best = l
		}
	}
	return best
}

type posSerialKey struct {
	Code string `json:"c"`
}

func (k posSerialKey) Validate() error {
	if k.Code == "" || len(k.Code) > 64 {
		return pagination.ErrInvalidCursor
	}
	return nil
}

// Bootstrap mengembalikan katalog produk aktif, price list POS + versinya,
// snapshot stok lokasi, halaman pertama serial UNIT IN_STOCK, dan kasir yang
// boleh login di device. Semua dibaca dalam satu transaksi REPEATABLE READ
// read-only (snapshot konsisten), satu query per bagian (tanpa N+1).
//
// Trade-off PIN kasir (PLANNING §6.2): pos_pin_hash (argon2id) ikut ke device
// supaya kasir bisa login saat offline. Device yang dicuri memungkinkan
// brute force PIN 6 digit secara offline. Dampaknya dibatasi: PIN hanya
// berlaku untuk operasi POS di device itu, setiap request ke server tetap
// memakai token device yang bisa dicabut, cashier_id di transaksi dicek ulang
// server (user aktif ber-scope pos.sync di lokasi itu), dan hanya user yang
// sudah punya PIN POS yang dikirim. Jangan pakai PIN yang sama dengan password.
func (s *PosService) Bootstrap(ctx context.Context, p *auth.Principal) (PosBootstrap, error) {
	deviceID, locID, err := posDevice(p)
	if err != nil {
		return PosBootstrap{}, err
	}
	var out PosBootstrap
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out.Now = s.clock.Now()
		d, err := s.repo.DeviceByID(tx, deviceID, false)
		if err != nil {
			return err
		}
		if d == nil || d.Status != "ACTIVE" {
			return ErrInvalidToken
		}
		out.Device = *d
		loc, err := s.repo.LocationByID(tx, locID)
		if err != nil {
			return err
		}
		if loc == nil {
			return ErrStockLocationNotFound
		}
		out.Location = *loc
		if out.Products, err = s.repo.ActiveProducts(tx); err != nil {
			return err
		}
		lists, err := s.repo.POSPriceLists(tx, locID)
		if err != nil {
			return err
		}
		if eff := posEffectiveList(lists, out.Now); eff != nil {
			items, err := s.repo.PriceListItems(tx, eff.ID)
			if err != nil {
				return err
			}
			out.PriceList = &PosEffectivePriceList{PosPriceListRow: *eff, Items: items}
		}
		if out.Stock, err = s.repo.LocationStock(tx, locID); err != nil {
			return err
		}
		if out.Cashiers, err = s.repo.CashiersAt(tx, locID); err != nil {
			return err
		}
		out.Serials, err = s.serialPage(tx, locID, "")
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}

// BootstrapSerials mengembalikan halaman serial berikutnya (cursor dari
// bootstrap atau halaman sebelumnya).
func (s *PosService) BootstrapSerials(ctx context.Context, p *auth.Principal, cursor string) (pagination.Page[repository.PosSerialRow], error) {
	_, locID, err := posDevice(p)
	if err != nil {
		return pagination.Page[repository.PosSerialRow]{}, err
	}
	var key posSerialKey
	if cursor != "" {
		if err := pagination.Decode(cursor, cursorPosSerials, &key); err != nil {
			return pagination.Page[repository.PosSerialRow]{}, apperror.Validation(apperror.FieldError{
				Field: pagination.FieldCursor, Code: "invalid_cursor", Message: "cursor tidak valid; mulai lagi dari halaman pertama"})
		}
	}
	return s.serialPage(s.db.WithContext(ctx), locID, key.Code)
}

func (s *PosService) serialPage(tx *gorm.DB, locID uuid.UUID, after string) (pagination.Page[repository.PosSerialRow], error) {
	rows, err := s.repo.InStockSerials(tx, locID, after, PosSerialPageSize)
	if err != nil {
		return pagination.Page[repository.PosSerialRow]{}, err
	}
	return pagination.Trim(rows, PosSerialPageSize, cursorPosSerials, func(r repository.PosSerialRow) any {
		return posSerialKey{Code: r.Code}
	})
}

// ------------------------------------------------------------------ heartbeat

// PosHeartbeatResult adalah keadaan sesudah heartbeat.
type PosHeartbeatResult struct {
	Now          time.Time
	SyncState    string
	LastAckedSeq int64
	ClockSkewMs  *int64
	// OnlineBlockedReason: kenapa heartbeat ini tidak menjadikan toko ONLINE
	// ("" = toko ONLINE): pending_count_missing, pending_outbox, missing_seqs.
	OnlineBlockedReason string
}

// Alasan heartbeat tidak menjadikan toko ONLINE.
const (
	PosOnlineBlockedPendingUnknown = "pending_count_missing"
	PosOnlineBlockedPendingOutbox  = "pending_outbox"
	PosOnlineBlockedMissingSeqs    = "missing_seqs"
)

// Heartbeat mencatat device masih hidup (pos_devices.last_seen_at, monoton).
// Toko baru dijadikan ONLINE (sync_state + last_heartbeat_at) kalau outbox
// device sudah habis: pending_count = 0 dan server tidak melihat gap seq
// device itu. Selama masih ada penjualan offline yang belum tersinkron, stok
// toko di server belum benar, jadi ATP online toko tetap 0; heartbeat seperti
// itu juga tidak memperpanjang ONLINE (job menandai OFFLINE setelah timeout).
// Klien: sync dulu, lalu heartbeat dengan pending_count = 0.
// Urutan lock: pos_devices → locations (sama dengan akhir sync).
func (s *PosService) Heartbeat(ctx context.Context, p *auth.Principal, clientTime *time.Time, pendingCount *int64) (PosHeartbeatResult, error) {
	deviceID, locID, err := posDevice(p)
	if err != nil {
		return PosHeartbeatResult{}, err
	}
	var out PosHeartbeatResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		out.Now = s.clock.Now()
		if err := s.repo.TouchDevice(tx, deviceID, out.Now); err != nil {
			return err
		}
		d, err := s.repo.DeviceByID(tx, deviceID, false)
		if err != nil {
			return err
		}
		if d == nil {
			return ErrInvalidToken
		}
		switch {
		case pendingCount == nil:
			out.OnlineBlockedReason = PosOnlineBlockedPendingUnknown
		case *pendingCount > 0:
			out.OnlineBlockedReason = PosOnlineBlockedPendingOutbox
		default:
			gap, err := s.repo.DeviceHasGap(tx, deviceID, d.LastAckedSeq)
			if err != nil {
				return err
			}
			if gap {
				out.OnlineBlockedReason = PosOnlineBlockedMissingSeqs
			}
		}
		if out.OnlineBlockedReason == "" {
			if err := s.repo.MarkLocationOnline(tx, locID, out.Now); err != nil {
				return err
			}
		}
		loc, err := s.repo.LocationByID(tx, locID)
		if err != nil {
			return err
		}
		out.LastAckedSeq, out.SyncState = d.LastAckedSeq, loc.SyncState
		return nil
	})
	if err != nil {
		return PosHeartbeatResult{}, err
	}
	if clientTime != nil {
		skew := out.Now.Sub(*clientTime).Milliseconds()
		out.ClockSkewMs = &skew
	}
	return out, nil
}

// MarkStaleLocationsOffline menandai lokasi ONLINE tanpa heartbeat lebih dari
// HeartbeatTimeout menjadi OFFLINE, sehingga ATP online toko itu 0 (PLANNING
// §7.4 lapis pencegahan). Lokasi yang tidak lagi punya device ACTIVE (semua
// dicabut) kembali NOT_TRACKED, bukan OFFLINE selamanya. Satu lokasi per
// transaksi, kandidat diambil dengan SKIP LOCKED + UPDATE bersyarat: aman
// dijalankan beberapa instance worker dan bersamaan dengan heartbeat.
// Mengembalikan jumlah lokasi yang berubah.
func (s *PosService) MarkStaleLocationsOffline(ctx context.Context) (int, error) {
	n := 0
	for n < posStaleBatch {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		now := s.clock.Now()
		var loc *repository.PosStaleLocation
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			loc, err = s.repo.MarkOneStaleLocationOffline(tx, now.Add(-s.cfg.HeartbeatTimeout), now)
			return err
		})
		if err != nil {
			return n, err
		}
		if loc == nil {
			return n, nil
		}
		n++
		s.log.InfoContext(ctx, "status sinkron lokasi POS diubah job heartbeat", "location_id", loc.ID, "sync_state", loc.SyncState)
	}
	return n, nil
}

// posInvalidSigningKey: signing_key_enc tidak bisa dibuka (PII_ENC_KEY
// berubah tanpa provisioning ulang). Kegagalan server, bukan klien.
func posInvalidSigningKey(err error) error {
	return apperror.Wrap(err, http.StatusServiceUnavailable, "signing_key_unavailable",
		"Signing key tidak bisa dibaca", "Signing key device tidak bisa dibuka server; hubungi admin untuk provisioning ulang.")
}

// posSafeInt: batas aman bilangan bulat JS untuk nilai di payload.
const posSafeInt = domain.MaxSafeInteger
