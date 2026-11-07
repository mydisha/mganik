package seed

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"mganik/api/internal/clock"
	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/pii"
)

// ErrDisabled dikembalikan kalau seed dipanggil tanpa SEED_DEMO=true.
var ErrDisabled = errors.New("seed demo ditolak: set SEED_DEMO=true untuk mengisi data demo")

// Options untuk Run.
type Options struct {
	// Enabled harus true (dari SEED_DEMO=true).
	Enabled bool
	// Pepper = CLAIM_PEPPER untuk claim_secret_hash.
	Pepper []byte
	// DemoPassword = SEED_DEMO_PASSWORD untuk semua akun demo.
	DemoPassword string
	// PasswordParams default domain.OWASPArgon2id kalau kosong.
	PasswordParams domain.Argon2Params
	Log            *slog.Logger
	// Clock menentukan "hari ini" untuk riwayat penjualan demo (default jam
	// sistem). Test memakai clock.Fake.
	Clock clock.Clock
	// PIIEncKey = PII_ENC_KEY untuk mengenkripsi signing key device POS demo.
	// Kosong = config.DemoPIIEncKey (sama dengan fallback router di luar
	// production; seed demo ditolak di production tanpa izin eksplisit).
	PIIEncKey []byte
}

// Result merangkum jumlah baris baru per tabel (0 semua saat seed diulang).
type Result struct {
	Inserted map[string]int64
	// ClaimHashesUpdated: unit demo yang claim_secret_hash-nya diganti karena
	// CLAIM_PEPPER berbeda dari seed sebelumnya.
	ClaimHashesUpdated int64
	// SigningKeysUpdated: device demo yang signing_key_enc-nya ditulis
	// (pertama kali, atau karena PII_ENC_KEY berubah).
	SigningKeysUpdated int64
}

// Total jumlah semua baris baru.
func (r Result) Total() int64 {
	var n int64
	for _, v := range r.Inserted {
		n += v
	}
	return n
}

// Run mengisi data demo dalam satu transaksi. Aman dijalankan berulang.
// PIN dan token tidak pernah ditulis ke log; pakai `api seed --print-demo`.
func Run(ctx context.Context, db *gorm.DB, opts Options) (Result, error) {
	if !opts.Enabled {
		return Result{}, ErrDisabled
	}
	if len(opts.Pepper) == 0 {
		return Result{}, errors.New("seed: CLAIM_PEPPER kosong")
	}
	if opts.DemoPassword == "" {
		return Result{}, errors.New("seed: SEED_DEMO_PASSWORD kosong")
	}
	if opts.PasswordParams == (domain.Argon2Params{}) {
		opts.PasswordParams = domain.OWASPArgon2id
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Clock == nil {
		opts.Clock = clock.Real{}
	}

	s := &seeder{opts: opts, res: Result{Inserted: map[string]int64{}}}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		s.tx = tx
		steps := []func() error{
			s.regions, s.catalog, s.parties, s.locations, s.territories,
			s.priceLists, s.users, s.serials, s.inventory, s.salesHistory, s.posDevice,
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("seed demo: %w", err)
	}
	opts.Log.Info("seed demo selesai", "baris_baru", s.res.Total(), "hash_klaim_diperbarui", s.res.ClaimHashesUpdated)
	return s.res, nil
}

type seeder struct {
	tx   *gorm.DB
	opts Options
	res  Result
}

// insert menulis baris dengan ON CONFLICT DO NOTHING (tanpa target: semua
// unique constraint), lalu mencatat jumlah baris yang benar-benar baru.
func insert[T interface{ TableName() string }](s *seeder, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	res := s.tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	if res.Error != nil {
		return fmt.Errorf("insert %s: %w", rows[0].TableName(), res.Error)
	}
	s.res.Inserted[rows[0].TableName()] += res.RowsAffected
	return nil
}

func ptr[T any](v T) *T { return &v }

func at(days int, hours int) time.Time {
	return demoEpoch.AddDate(0, 0, days).Add(time.Duration(hours) * time.Hour)
}

// ------------------------------------------------------------------ wilayah

type regionSeed struct {
	code, name, level string
	lat, lng          float64
}

// Kode Kemendagri; centroid perkiraan (cukup untuk resolusi titik terdekat).
var provinces = []regionSeed{
	{"12", "Sumatera Utara", "PROVINCE", 2.1154, 99.5451},
	{"31", "DKI Jakarta", "PROVINCE", -6.2088, 106.8456},
	{"32", "Jawa Barat", "PROVINCE", -6.9147, 107.6098},
	{"33", "Jawa Tengah", "PROVINCE", -7.1510, 110.1403},
	{"35", "Jawa Timur", "PROVINCE", -7.5361, 112.2384},
	{"51", "Bali", "PROVINCE", -8.4095, 115.1889},
	{"73", "Sulawesi Selatan", "PROVINCE", -3.6688, 119.9741},
}

var cities = []regionSeed{
	{"12.71", "Kota Medan", "CITY", 3.5952, 98.6722},
	{"31.71", "Kota Jakarta Selatan", "CITY", -6.2615, 106.8106},
	{"31.74", "Kota Jakarta Barat", "CITY", -6.1674, 106.7637},
	{"32.73", "Kota Bandung", "CITY", -6.9175, 107.6191},
	{"32.75", "Kota Bekasi", "CITY", -6.2383, 106.9756},
	{"33.74", "Kota Semarang", "CITY", -7.0051, 110.4381},
	{"35.15", "Kab. Sidoarjo", "REGENCY", -7.4478, 112.7183},
	{"35.73", "Kota Malang", "CITY", -7.9666, 112.6326},
	{"35.78", "Kota Surabaya", "CITY", -7.2575, 112.7521},
	{"51.71", "Kota Denpasar", "CITY", -8.6705, 115.2126},
	{"73.06", "Kab. Gowa", "REGENCY", -5.3103, 119.7426},
	{"73.71", "Kota Makassar", "CITY", -5.1477, 119.4327},
}

func (s *seeder) regions() error {
	toModel := func(in []regionSeed, withParent bool) []model.Region {
		out := make([]model.Region, 0, len(in))
		for _, r := range in {
			m := model.Region{Code: r.code, Name: r.name, Level: r.level, CentroidLat: ptr(r.lat), CentroidLng: ptr(r.lng)}
			if withParent {
				m.ParentCode = ptr(r.code[:2])
			}
			out = append(out, m)
		}
		return out
	}
	if err := insert(s, toModel(provinces, false)); err != nil {
		return err
	}
	return insert(s, toModel(cities, true))
}

// ------------------------------------------------------------------ katalog

func (s *seeder) catalog() error {
	brandID := ID("brand:" + BrandCode)
	if err := insert(s, []model.Brand{{ID: brandID, Code: BrandCode, Name: "Demo Organik"}}); err != nil {
		return err
	}
	// Nama produk generik; nomor BPOM/halal fiktif untuk demo.
	products := []model.Product{
		{
			ID: ProductID(SKU1), BrandID: brandID, SKU: SKU1, GTIN: ptr("8999999000011"),
			Name: "Minuman Serbuk Herbal Jahe 10 Sachet", UOM: "BOX", ShelfLifeDays: ptr(730),
			Attributes: model.MustJSON(map[string]any{
				"bpom_no": "MD 000000000001", "halal": true, "halal_cert_no": "ID00000000000001",
				"flavor": "jahe", "net_weight_g": 250,
			}),
		},
		{
			ID: ProductID(SKU2), BrandID: brandID, SKU: SKU2, GTIN: ptr("8999999000028"),
			Name: "Teh Herbal Celup Rosella 20 Kantong", UOM: "BOX", ShelfLifeDays: ptr(540),
			Attributes: model.MustJSON(map[string]any{
				"bpom_no": "MD 000000000002", "halal": true, "halal_cert_no": "ID00000000000002",
				"flavor": "rosella", "net_weight_g": 40,
			}),
		},
	}
	if err := insert(s, products); err != nil {
		return err
	}
	var specs []model.PackagingSpec
	for _, sku := range []string{SKU1, SKU2} {
		specs = append(specs,
			model.PackagingSpec{ID: ID("packaging:" + sku + ":UNIT"), ProductID: ProductID(sku), ChildLevel: "UNIT", ParentLevel: "CARTON", QtyPerParent: UnitsPerCarton},
			model.PackagingSpec{ID: ID("packaging:" + sku + ":CARTON"), ProductID: ProductID(sku), ChildLevel: "CARTON", ParentLevel: "PALLET", QtyPerParent: CartonsPerPallet},
		)
	}
	return insert(s, specs)
}

// ------------------------------------------------------------------ party

func (s *seeder) parties() error {
	parties := []model.Party{
		{ID: ID(CompanyPartyKey), Kind: "ORG", Name: "PT Demo Organik Nusantara", LegalID: ptr("00.000.000.0-000.000"), Attributes: model.JSON(`{}`)},
		{ID: ID(JatimPartyKey), Kind: "ORG", Name: "CV Distributor Jatim (Demo)", Attributes: model.JSON(`{"region":"Jawa Timur"}`)},
		{ID: ID(SulselPartyKey), Kind: "ORG", Name: "CV Distributor Sulsel (Demo)", Attributes: model.JSON(`{"region":"Sulawesi Selatan","subsidy_program":"SUBSIDI_SULSEL_Q4"}`)},
	}
	if err := insert(s, parties); err != nil {
		return err
	}
	from := at(-365, 0)
	return insert(s, []model.PartyRole{
		{ID: ID("party_role:company:BRAND_OWNER"), PartyID: ID(CompanyPartyKey), RoleCode: "BRAND_OWNER", ValidFrom: from},
		{ID: ID("party_role:company:MANUFACTURER"), PartyID: ID(CompanyPartyKey), RoleCode: "MANUFACTURER", ValidFrom: from},
		{ID: ID("party_role:jatim:DISTRIBUTOR"), PartyID: ID(JatimPartyKey), RoleCode: "DISTRIBUTOR", ValidFrom: from},
		{ID: ID("party_role:sulsel:DISTRIBUTOR"), PartyID: ID(SulselPartyKey), RoleCode: "DISTRIBUTOR", ValidFrom: from},
	})
}

// ------------------------------------------------------------------ lokasi

func (s *seeder) locations() error {
	company, jatim, sulsel := ID(CompanyPartyKey), ID(JatimPartyKey), ID(SulselPartyKey)
	loc := func(code, name, typ string, party uuid.UUID, region string, lat, lng float64, tz string) model.Location {
		return model.Location{
			ID: LocationID(code), Code: code, Name: name, TypeCode: typ, PartyID: ptr(party),
			RegionCode: ptr(region), Lat: ptr(lat), Lng: ptr(lng), Timezone: tz, SyncState: model.SyncNotTracked,
		}
	}
	central := loc(CentralWHCode, "Gudang Pusat Bekasi", "CENTRAL_WH", company, "32.75", -6.2349, 106.9896, "Asia/Jakarta")
	central.OnlineFulfillmentEnabled = true
	hub := loc(HubSurabayaCode, "Hub Surabaya", "HUB", company, "35.78", -7.2756, 112.7426, "Asia/Jakarta")
	hub.OnlineFulfillmentEnabled = true
	// Toko di-seed OFFLINE: ATP online toko = 0 sampai device mengirim heartbeat
	// dengan outbox kosong. Toko Bandung belum punya device POS, jadi saat worker
	// berjalan, job heartbeat memindahkannya ke NOT_TRACKED (lokasi tanpa device
	// ACTIVE tidak dilacak). Test memakai status awal OFFLINE ini sebagai skenario.
	jaksel := loc(StoreJakselCode, "Toko Jakarta Selatan", "OWN_STORE", company, "31.71", -6.2441, 106.8003, "Asia/Jakarta")
	jaksel.OnlineFulfillmentEnabled, jaksel.SyncState = true, model.SyncOffline
	bandung := loc(StoreBandungCode, "Toko Bandung", "OWN_STORE", company, "32.73", -6.9039, 107.6186, "Asia/Jakarta")
	bandung.OnlineFulfillmentEnabled, bandung.SyncState = true, model.SyncOffline
	pJatim := loc(PartnerJatimCode, "Gudang CV Distributor Jatim", "PARTNER_WH", jatim, "35.78", -7.3305, 112.7300, "Asia/Jakarta")
	pSulsel := loc(PartnerSulselCode, "Gudang CV Distributor Sulsel", "PARTNER_WH", sulsel, "73.71", -5.1355, 119.4238, "Asia/Makassar")
	// Lokasi virtual untuk barang dalam perjalanan: tanpa wilayah/koordinat.
	transit := model.Location{
		ID: LocationID(InTransitCode), Code: InTransitCode, Name: "Dalam Perjalanan (virtual)", TypeCode: "IN_TRANSIT",
		PartyID: ptr(company), Timezone: "Asia/Jakarta", SyncState: model.SyncNotTracked,
	}
	return insert(s, []model.Location{central, hub, jaksel, bandung, pJatim, pSulsel, transit})
}

// ------------------------------------------------------------------ wilayah distributor

func (s *seeder) territories() error {
	jatimTer, sulselTer := ID("territory:JATIM"), ID("territory:SULSEL")
	if err := insert(s, []model.Territory{
		{ID: jatimTer, Code: "TER-JATIM", Name: "Jawa Timur (Surabaya, Sidoarjo, Malang)"},
		{ID: sulselTer, Code: "TER-SULSEL", Name: "Sulawesi Selatan (Makassar, Gowa)"},
	}); err != nil {
		return err
	}
	if err := insert(s, []model.TerritoryRegion{
		{TerritoryID: jatimTer, RegionCode: "35.78"},
		{TerritoryID: jatimTer, RegionCode: "35.15"},
		{TerritoryID: jatimTer, RegionCode: "35.73"},
		{TerritoryID: sulselTer, RegionCode: "73.71"},
		{TerritoryID: sulselTer, RegionCode: "73.06"},
	}); err != nil {
		return err
	}
	from := at(-365, 0)
	// Mode eksplisit: pemilik brand NATIONAL (tanpa territory), distributor SELECTED.
	return insert(s, []model.PartnerTerritory{
		{ID: ID("partner_territory:company"), PartyID: ID(CompanyPartyKey), CoverageMode: model.CoverageNational, ValidFrom: from},
		{ID: ID("partner_territory:jatim"), PartyID: ID(JatimPartyKey), TerritoryID: ptr(jatimTer), CoverageMode: model.CoverageSelected, ValidFrom: from},
		{ID: ID("partner_territory:sulsel"), PartyID: ID(SulselPartyKey), TerritoryID: ptr(sulselTer), CoverageMode: model.CoverageSelected, ValidFrom: from},
	})
}

// ------------------------------------------------------------------ harga

func (s *seeder) priceLists() error {
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	lists := []model.PriceList{
		{ID: ID("price_list:POS:1"), Code: "POS-2026-10", Name: "Harga POS Oktober 2026", ChannelCode: "POS", Version: 1, Currency: "IDR", ValidFrom: oct, ValidTo: ptr(nov)},
		{ID: ID("price_list:POS:2"), Code: "POS-2026-11", Name: "Harga POS November 2026", ChannelCode: "POS", Version: 2, Currency: "IDR", ValidFrom: nov},
		{ID: ID("price_list:D2C_WEB:1"), Code: "D2C-2026-10", Name: "Harga website D2C", ChannelCode: "D2C_WEB", Version: 1, Currency: "IDR", ValidFrom: oct},
	}
	if err := insert(s, lists); err != nil {
		return err
	}
	item := func(list string, sku string, price int64) model.PriceListItem {
		return model.PriceListItem{PriceListID: ID(list), ProductID: ProductID(sku), Price: price}
	}
	return insert(s, []model.PriceListItem{
		item("price_list:POS:1", SKU1, 45000), item("price_list:POS:1", SKU2, 32000),
		item("price_list:POS:2", SKU1, 47500), item("price_list:POS:2", SKU2, 33000),
		item("price_list:D2C_WEB:1", SKU1, 46000), item("price_list:D2C_WEB:1", SKU2, 33000),
	})
}

// ------------------------------------------------------------------ user & RBAC

func (s *seeder) users() error {
	// Hash argon2id hanya untuk akun yang belum ada: seed ulang tidak mengubah
	// password dan tidak membayar biaya hash lagi.
	ids := make([]uuid.UUID, len(DemoAccounts))
	for i, a := range DemoAccounts {
		ids[i] = UserID(a.Email)
	}
	var existing []uuid.UUID
	if err := s.tx.Model(&model.User{}).Where("id IN ?", ids).Pluck("id", &existing).Error; err != nil {
		return fmt.Errorf("membaca user demo: %w", err)
	}
	have := make(map[uuid.UUID]bool, len(existing))
	for _, id := range existing {
		have[id] = true
	}

	var users []model.User
	for _, a := range DemoAccounts {
		if have[UserID(a.Email)] {
			continue
		}
		hash, err := domain.HashPassword(s.opts.DemoPassword, s.opts.PasswordParams)
		if err != nil {
			return err
		}
		u := model.User{ID: UserID(a.Email), Email: a.Email, Name: a.Name, PasswordHash: hash, Status: "ACTIVE"}
		if a.Role == "CASHIER" {
			pin, err := domain.HashPassword(DemoCashierPIN, s.opts.PasswordParams)
			if err != nil {
				return err
			}
			u.PosPINHash = &pin
		}
		users = append(users, u)
	}
	if err := insert(s, users); err != nil {
		return err
	}

	admin := UserID(DemoAccounts[0].Email)
	var roles []model.UserRole
	var scopes []model.UserRoleLocation
	for _, a := range DemoAccounts {
		urID := ID("user_role:" + a.Email + ":" + a.Role)
		roles = append(roles, model.UserRole{ID: urID, UserID: UserID(a.Email), RoleCode: a.Role, ScopeMode: a.ScopeMode, GrantedBy: ptr(admin)})
		if a.ScopeMode == model.ScopeSelected {
			for _, code := range a.Locations {
				scopes = append(scopes, model.UserRoleLocation{UserRoleID: urID, LocationID: LocationID(code)})
			}
		}
	}
	if err := insert(s, roles); err != nil {
		return err
	}
	return insert(s, scopes)
}

// ------------------------------------------------------------------ serialisasi

func (s *seeder) serials() error {
	sku := ProductID(SKU1)
	operator := UserID("produksi@mganik.test")
	warehouse := UserID("gudang@mganik.test")
	central := LocationID(CentralWHCode)
	company := ID(CompanyPartyKey)

	codeBatch := func(level string, qty int) model.CodeBatch {
		return model.CodeBatch{
			ID: ID("code_batch:" + level), LevelCode: level, ProductID: ptr(sku), Qty: qty,
			PrinterVendor: "Vendor Label Demo", IssuedAt: at(-10, 0), DeliveredAt: ptr(at(-5, 0)),
			ReconciledAt: ptr(at(1, 0)), CreatedBy: ptr(operator),
		}
	}
	// Kode unit toko (DemoStoreUnit) dicetak di code batch sendiri, jadi code
	// batch UNIT palet tetap berisi tepat DemoUnitCount kode.
	storeCodeBatch := codeBatch("UNIT", 1)
	storeCodeBatch.ID = ID("code_batch:UNIT:" + StoreJakselCode)
	if err := insert(s, []model.CodeBatch{
		codeBatch("PALLET", 1), codeBatch("CARTON", CartonsPerPallet), codeBatch("UNIT", DemoUnitCount), storeCodeBatch,
	}); err != nil {
		return err
	}

	if err := insert(s, []model.Batch{{
		ID: BatchID(), ProductID: sku, LotNo: DemoLotNo, Line: ptr("LINE-1"),
		MfgDate: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), ExpDate: time.Date(2028, 10, 19, 0, 0, 0, 0, time.UTC),
		Status: "ACTIVE",
	}}); err != nil {
		return err
	}

	commissioned := at(0, 0)
	serial := func(level string, i int, parent *uuid.UUID) model.SerialUnit {
		return model.SerialUnit{
			ID: DemoSerialID(level, i), Code: DemoSerialCode(level, i), LevelCode: level,
			ProductID: ptr(sku), BatchID: ptr(BatchID()), CodeBatchID: ptr(ID("code_batch:" + level)),
			ParentID: parent, Status: "IN_STOCK", LocationID: ptr(central), CustodianPartyID: ptr(company),
			CommissionedAt: ptr(commissioned), Attributes: model.JSON(`{}`),
		}
	}

	palletID := DemoSerialID("PALLET", 0)
	pallet := []model.SerialUnit{serial("PALLET", 0, nil)}
	var cartons []model.SerialUnit
	for c := range CartonsPerPallet {
		cartons = append(cartons, serial("CARTON", c, ptr(palletID)))
	}
	var units []model.SerialUnit
	for _, u := range DemoUnits() {
		su := serial("UNIT", u.Index, ptr(DemoSerialID("CARTON", u.Index/UnitsPerCarton)))
		su.ClaimSecretHash = domain.ClaimSecretHash(s.opts.Pepper, u.ID, u.PIN)
		units = append(units, su)
	}
	// Unit lepas di Toko Jakarta Selatan: tanpa karton, custodian = party toko
	// (perusahaan). Saldo SKU1 toko (1, Kasus 4) adalah unit ini.
	store := DemoStoreUnit()
	storeUnit := serial("UNIT", store.Index, nil)
	storeUnit.CodeBatchID = ptr(storeCodeBatch.ID)
	storeUnit.LocationID = ptr(LocationID(StoreJakselCode))
	storeUnit.ClaimSecretHash = domain.ClaimSecretHash(s.opts.Pepper, store.ID, store.PIN)
	units = append(units, storeUnit)
	// Induk dulu supaya FK parent_id terpenuhi.
	for _, batch := range [][]model.SerialUnit{pallet, cartons} {
		if err := insert(s, batch); err != nil {
			return err
		}
	}
	// Unit demo: kolom lain DO NOTHING, tetapi claim_secret_hash selalu
	// disamakan dengan CLAIM_PEPPER yang aktif. Kalau pepper berganti (misal
	// seed pertama dari compose, lalu `make api` dengan .env lain), seed ulang
	// memperbaiki hash fixture yang basi. Hanya fixture demo (ID deterministik)
	// yang tersentuh; WHERE membuat seed ulang dengan pepper sama tetap no-op.
	ids := make([]uuid.UUID, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}
	var existing int64
	if err := s.tx.Model(&model.SerialUnit{}).Where("id IN ?", ids).Count(&existing).Error; err != nil {
		return fmt.Errorf("menghitung unit demo: %w", err)
	}
	res := s.tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Set{
			{Column: clause.Column{Name: "claim_secret_hash"}, Value: clause.Expr{SQL: "EXCLUDED.claim_secret_hash"}},
			{Column: clause.Column{Name: "updated_at"}, Value: clause.Expr{SQL: "now()"}},
		},
		Where: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "serial_units.claim_secret_hash IS DISTINCT FROM EXCLUDED.claim_secret_hash"},
		}},
	}).Create(&units)
	if res.Error != nil {
		return fmt.Errorf("insert serial_units (unit): %w", res.Error)
	}
	// RowsAffected = unit baru + unit yang hash-nya diperbaiki.
	fresh := int64(len(units)) - existing
	s.res.Inserted["serial_units"] += fresh
	s.res.ClaimHashesUpdated += res.RowsAffected - fresh

	// Riwayat EPCIS: commission di line produksi gudang pusat → packing (unit ke
	// karton, karton ke palet) → diterima di lokasinya. Unit toko tidak dikemas
	// dan diterima langsung oleh toko sehari kemudian, sehingga ledger toko
	// (RECEIPT 1) cocok dengan event-nya dan ledger gudang pusat tidak berubah.
	var events []model.UnitEvent
	for _, su := range append(append(pallet, cartons...), units...) {
		ev := func(step string, from, to *string, parent *uuid.UUID, loc, actor uuid.UUID, hours int) model.UnitEvent {
			return model.UnitEvent{
				ID: ID("unit_event:" + su.Code + ":" + step), SerialUnitID: su.ID, BizStep: step,
				FromStatus: from, ToStatus: to, ParentID: parent, LocationID: ptr(loc), PartyID: ptr(company),
				ActorType: model.ActorUser, ActorID: ptr(actor), RefType: ptr("BATCH"), RefID: ptr(BatchID()),
				Payload: model.JSON(`{"source":"seed"}`), OccurredAt: at(0, hours), RecordedAt: at(0, hours),
			}
		}
		events = append(events, ev("commissioning", ptr("GENERATED"), ptr("COMMISSIONED"), nil, central, operator, 0))
		if su.ParentID != nil {
			events = append(events, ev("packing", nil, nil, su.ParentID, central, operator, 1))
		}
		if su.ID == store.ID {
			events = append(events, ev("receiving", ptr("COMMISSIONED"), ptr("IN_STOCK"), nil,
				LocationID(StoreJakselCode), UserID("kepala.jaksel@mganik.test"), 26))
			continue
		}
		events = append(events, ev("receiving", ptr("COMMISSIONED"), ptr("IN_STOCK"), nil, central, warehouse, 2))
	}
	return insert(s, events)
}

// ------------------------------------------------------------------ stok

type openingBalance struct {
	location, sku string
	qty           int64
	refType       string
	refID         uuid.UUID
}

// Saldo awal. Saldo SKU1 di gudang pusat = 48 unit serial palet demo, dan saldo
// SKU1 di Toko Jakarta Selatan = 1 unit serial lepas (DemoStoreUnit); keduanya
// dari batch demo. Saldo lain adalah stok pembuka sebelum program serialisasi
// (tanpa serial per unit).
var openingBalances = []openingBalance{
	{CentralWHCode, SKU1, DemoUnitCount, "BATCH", BatchID()},
	{CentralWHCode, SKU2, 100, "OPENING_BALANCE", ID("opening:" + CentralWHCode + ":" + SKU2)},
	// Kasus 4: tepat satu unit terakhir di toko, diperebutkan POS offline dan order
	// online. ID dan idempotency key ledger sama dengan versi seed sebelum unit ini
	// punya serial, jadi seed ulang di database lama tidak menambah saldo.
	{StoreJakselCode, SKU1, 1, "BATCH", BatchID()},
	{StoreJakselCode, SKU2, 12, "OPENING_BALANCE", ID("opening:" + StoreJakselCode + ":" + SKU2)},
	{StoreBandungCode, SKU1, 6, "OPENING_BALANCE", ID("opening:" + StoreBandungCode + ":" + SKU1)},
	{StoreBandungCode, SKU2, 8, "OPENING_BALANCE", ID("opening:" + StoreBandungCode + ":" + SKU2)},
}

func (s *seeder) inventory() error {
	var balances []model.InventoryBalance
	var moves []model.InventoryMovement
	warehouse := UserID("gudang@mganik.test")
	for _, ob := range openingBalances {
		loc, prod := LocationID(ob.location), ProductID(ob.sku)
		balances = append(balances, model.InventoryBalance{LocationID: loc, ProductID: prod, PhysicalQty: ob.qty})
		// Setiap saldo awal punya baris ledger RECEIPT: SUM(delta) = saldo.
		moves = append(moves, model.InventoryMovement{
			ID: ID("movement:receipt:" + ob.location + ":" + ob.sku), LocationID: loc, ProductID: prod,
			MovementType: "RECEIPT", PhysicalDelta: ob.qty, RefType: ob.refType, RefID: ob.refID,
			IdempotencyKey: "seed:receipt:" + ob.location + ":" + ob.sku, Reason: ptr("Saldo awal data demo"),
			ActorType: model.ActorUser, ActorID: ptr(warehouse), OccurredAt: at(0, 2), RecordedAt: at(0, 2),
		})
	}
	if err := insert(s, balances); err != nil {
		return err
	}
	if err := insert(s, moves); err != nil {
		return err
	}

	policy := func(loc, sku string, minQty, buffer int64) model.StockPolicy {
		return model.StockPolicy{
			LocationID: LocationID(loc), ProductID: ProductID(sku),
			MinQty: minQty, OnlineBufferQty: buffer, LeadTimeDays: 3, SafetyDays: 2,
		}
	}
	return insert(s, []model.StockPolicy{
		// Gudang pusat: lead time produksi lebih panjang, untuk demo DOI gudang.
		{LocationID: LocationID(CentralWHCode), ProductID: ProductID(SKU1), MinQty: 24, LeadTimeDays: 14, SafetyDays: 7},
		{LocationID: LocationID(CentralWHCode), ProductID: ProductID(SKU2), MinQty: 48, LeadTimeDays: 14, SafetyDays: 7},
		// Buffer 0 untuk SKU1 di Jakarta Selatan supaya unit terakhir terlihat
		// online dan skenario Kasus 4 bisa didemokan.
		policy(StoreJakselCode, SKU1, 3, 0),
		policy(StoreJakselCode, SKU2, 4, 2),
		policy(StoreBandungCode, SKU1, 3, 2),
		policy(StoreBandungCode, SKU2, 4, 2),
	})
}

// ------------------------------------------------------------------ POS

func (s *seeder) posDevice() error {
	if err := insert(s, []model.PosDevice{{
		ID: PosDeviceID(), LocationID: LocationID(StoreJakselCode), Name: "Kasir 1 Toko Jakarta Selatan",
		TokenHash: DemoDeviceTokenHash(), Status: "ACTIVE",
		ProvisionedBy: ptr(UserID("kepala.jaksel@mganik.test")),
	}}); err != nil {
		return err
	}
	return s.posDeviceSigningKey()
}

// posDeviceSigningKey memastikan signing_key_enc device demo terbuka dengan
// PII_ENC_KEY aktif menjadi DemoDeviceSigningKey. Ciphertext AES-GCM memakai
// nonce acak, jadi perbandingan dilakukan setelah dekripsi: kolom hanya
// ditulis ulang kalau masih NULL, dienkripsi dengan kunci lain, atau isinya
// berbeda (seed ulang dengan kunci aktif selalu menghasilkan nilai cocok).
func (s *seeder) posDeviceSigningKey() error {
	master := s.opts.PIIEncKey
	if len(master) == 0 {
		master = []byte(config.DemoPIIEncKey)
	}
	c, err := pii.NewSigningKeyCipher(master)
	if err != nil {
		return fmt.Errorf("seed signing key device: %w", err)
	}
	id := PosDeviceID()
	var rows []struct{ SigningKeyEnc []byte }
	if err := s.tx.Raw(`SELECT signing_key_enc FROM pos_devices WHERE id = ? FOR UPDATE`, id).Scan(&rows).Error; err != nil {
		return fmt.Errorf("membaca device demo: %w", err)
	}
	if len(rows) == 1 && rows[0].SigningKeyEnc != nil {
		if got, err := c.Open(rows[0].SigningKeyEnc, id[:]); err == nil && hmac.Equal(got, DemoDeviceSigningKey()) {
			return nil
		}
	}
	enc, err := c.Seal(DemoDeviceSigningKey(), id[:])
	if err != nil {
		return err
	}
	res := s.tx.Exec(`UPDATE pos_devices SET signing_key_enc = ?::bytea, version = version + 1, updated_at = now() WHERE id = ?`, enc, id)
	if res.Error != nil {
		return fmt.Errorf("menyimpan signing key device demo: %w", res.Error)
	}
	s.res.SigningKeysUpdated += res.RowsAffected
	return nil
}
