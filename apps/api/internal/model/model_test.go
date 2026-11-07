package model_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"mganik/api/internal/model"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

type tabler interface{ TableName() string }

// allModels: satu entri per tabel migrasi (kecuali goose_db_version).
var allModels = []tabler{
	model.OutboxEvent{}, model.IdempotencyKey{}, model.AuditLog{},
	model.PackagingLevel{}, model.Channel{}, model.LocationType{}, model.RoleType{},
	model.Brand{}, model.Product{}, model.PackagingSpec{}, model.Region{},
	model.Party{}, model.PartyRole{}, model.Location{}, model.Territory{},
	model.TerritoryRegion{}, model.PartnerTerritory{}, model.PriceList{}, model.PriceListItem{},
	model.User{}, model.Session{}, model.Role{}, model.Permission{}, model.RolePermission{},
	model.UserRole{}, model.UserRoleLocation{}, model.Consumer{},
	model.CodeBatch{}, model.Batch{}, model.SerialUnitStatus{}, model.LifecycleTransition{},
	model.SerialUnit{}, model.UnitEvent{}, model.Shipment{}, model.ShipmentItem{},
	model.MovementType{}, model.InventoryBalance{}, model.InventoryMovement{}, model.StockPolicy{},
	model.OrderStatus{}, model.Order{}, model.OrderLine{}, model.OrderLineSerial{},
	model.OrderStatusHistory{}, model.Payment{}, model.StockReservation{},
	model.PosDevice{}, model.PosTransaction{}, model.AllocationConflict{},
	model.ScanEvent{}, model.AnomalyRule{}, model.Anomaly{}, model.LoyaltyClaim{}, model.LoyaltyLedgerEntry{},
}

// TestModels_MirrorSchema: setiap tabel migrasi punya model, dan kolom model
// sama persis dengan kolom tabel (tanpa AutoMigrate, drift ketahuan di sini).
func TestModels_MirrorSchema(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()

	type col struct{ TableName, ColumnName, IsNullable string }
	var cols []col
	// Generated column (available_qty) dilaporkan nullable walau tidak pernah NULL.
	if err := db.WithContext(ctx).Raw(`
		SELECT table_name, column_name,
		       CASE WHEN is_generated = 'ALWAYS' THEN 'NO' ELSE is_nullable END AS is_nullable
		  FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name <> 'goose_db_version'`).Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	if len(cols) == 0 {
		t.Fatal("information_schema kosong")
	}
	dbCols := map[string]map[string]string{}
	for _, c := range cols {
		if dbCols[c.TableName] == nil {
			dbCols[c.TableName] = map[string]string{}
		}
		dbCols[c.TableName][c.ColumnName] = c.IsNullable
	}

	seen := map[string]bool{}
	cache := &sync.Map{}
	for _, m := range allModels {
		s, err := schema.Parse(m, cache, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", m, err)
		}
		table := m.TableName()
		seen[table] = true
		want, ok := dbCols[table]
		if !ok {
			t.Errorf("%T: tabel %s tidak ada di migrasi", m, table)
			continue
		}
		modelCols := map[string]bool{}
		for _, f := range s.Fields {
			if f.DBName == "" {
				continue
			}
			modelCols[f.DBName] = true
			nullable, ok := want[f.DBName]
			if !ok {
				t.Errorf("%s: kolom model %s tidak ada di tabel", table, f.DBName)
				continue
			}
			// Kolom NULLABLE harus dipetakan ke tipe yang bisa nil.
			if nullable == "YES" && !canBeNil(f) {
				t.Errorf("%s.%s nullable di DB tetapi field %s (%s) tidak bisa nil", table, f.DBName, f.Name, f.FieldType)
			}
		}
		for name := range want {
			if !modelCols[name] {
				t.Errorf("%s: kolom tabel %s tidak dipetakan di model", table, name)
			}
		}
	}
	var missing []string
	for table := range dbCols {
		if !seen[table] {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("tabel tanpa model: %v", missing)
	}
}

func canBeNil(f *schema.Field) bool {
	switch f.FieldType.Kind().String() {
	case "ptr", "slice", "map", "interface":
		return true
	}
	return false
}

// TestModels_RoundTrip: tipe khusus (jsonb, text[], generated column, date)
// bisa ditulis dan dibaca lewat GORM.
func TestModels_RoundTrip(t *testing.T) {
	db := testutil.NewDB(t)
	brand := model.Brand{ID: uuid.New(), Code: "B1", Name: "Brand"}
	product := model.Product{
		ID: uuid.New(), BrandID: brand.ID, SKU: "SKU-1", Name: "Produk",
		Attributes: model.MustJSON(map[string]any{"halal": true, "bpom_no": "MD 1", "nama": "jahé ✓"}),
	}
	loc := model.Location{ID: uuid.New(), Code: "L1", Name: "Lokasi", TypeCode: "OWN_STORE"}
	mustCreate(t, db, &brand, &product, &loc)

	var gotProduct model.Product
	if err := db.First(&gotProduct, "id = ?", product.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotProduct.UOM != "PCS" || gotProduct.Version != 0 || gotProduct.CreatedAt.IsZero() {
		t.Fatalf("default DB tidak terbaca: %+v", gotProduct)
	}
	if string(gotProduct.Attributes) != `{"nama": "jahé ✓", "halal": true, "bpom_no": "MD 1"}` {
		t.Fatalf("attributes %s", gotProduct.Attributes)
	}

	var gotLoc model.Location
	if err := db.First(&gotLoc, "id = ?", loc.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotLoc.SyncState != model.SyncNotTracked || gotLoc.Timezone != "Asia/Jakarta" {
		t.Fatalf("default lokasi salah: %s %s", gotLoc.SyncState, gotLoc.Timezone)
	}

	// available_qty dihitung DB dan diabaikan saat ditulis.
	bal := model.InventoryBalance{LocationID: loc.ID, ProductID: product.ID, PhysicalQty: 10, ReservedQty: 3, QuarantineQty: 2, AvailableQty: 999}
	mustCreate(t, db, &bal)
	var gotBal model.InventoryBalance
	if err := db.First(&gotBal, "location_id = ? AND product_id = ?", loc.ID, product.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotBal.AvailableQty != 5 {
		t.Fatalf("available_qty %d, ingin 5", gotBal.AvailableQty)
	}
	if err := db.Model(&gotBal).Where("location_id = ? AND product_id = ?", loc.ID, product.ID).
		Updates(map[string]any{"reserved_qty": 4}).Error; err != nil {
		t.Fatal(err)
	}

	// text[] dengan karakter yang perlu di-quote, dan jsonb default.
	scan := model.ScanEvent{
		ID: uuid.New(), RawCode: "KODE", Context: "CONSUMER_VERIFY", Result: "NOT_FOUND",
		RuleHits: model.StringArray{"UNKNOWN_CODE", `koma, "kutip"`, "unicode ✓"}, OccurredAt: time.Now(),
	}
	empty := model.ScanEvent{ID: uuid.New(), RawCode: "KODE2", Context: "TEST_BUY", Result: "NOT_FOUND", OccurredAt: time.Now()}
	mustCreate(t, db, &scan, &empty)
	var gotScan, gotEmpty model.ScanEvent
	mustFirst(t, db, &gotScan, scan.ID)
	mustFirst(t, db, &gotEmpty, empty.ID)
	if len(gotScan.RuleHits) != 3 || gotScan.RuleHits[1] != `koma, "kutip"` || gotScan.RuleHits[2] != "unicode ✓" {
		t.Fatalf("rule_hits %q", gotScan.RuleHits)
	}
	if len(gotEmpty.RuleHits) != 0 || gotEmpty.GeoSource != "NONE" {
		t.Fatalf("default scan salah: %q %s", gotEmpty.RuleHits, gotEmpty.GeoSource)
	}

	// Kolom date dan JSON '[]' default.
	batch := model.Batch{ID: uuid.New(), ProductID: product.ID, LotNo: "LOT", MfgDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), ExpDate: time.Date(2028, 10, 1, 0, 0, 0, 0, time.UTC)}
	mustCreate(t, db, &batch)
	var gotBatch model.Batch
	mustFirst(t, db, &gotBatch, batch.ID)
	if gotBatch.Status != "ACTIVE" || gotBatch.ExpDate.Format(time.DateOnly) != "2028-10-01" {
		t.Fatalf("batch %s %s", gotBatch.Status, gotBatch.ExpDate)
	}

	// Identity lifecycle_transitions diisi DB.
	tr := model.LifecycleTransition{EntityType: model.EntitySerialUnit, FromCode: ptr("SOLD"), ToCode: "QUARANTINED", BizStep: "test"}
	mustCreate(t, db, &tr)
	if tr.ID == 0 {
		t.Fatal("ID identity tidak terisi dari RETURNING")
	}
}

func mustCreate(t *testing.T, db *gorm.DB, values ...any) {
	t.Helper()
	for _, v := range values {
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create %T: %v", v, err)
		}
	}
}

func mustFirst(t *testing.T, db *gorm.DB, dest any, id uuid.UUID) {
	t.Helper()
	if err := db.First(dest, "id = ?", id).Error; err != nil {
		t.Fatalf("membaca %T %s: %v", dest, id, err)
	}
}

func ptr[T any](v T) *T { return &v }
