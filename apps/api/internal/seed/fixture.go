// Package seed mengisi data demo (produk, lokasi, distributor, wilayah, serial,
// stok, user, device POS). Seed idempotent: ID deterministik + ON CONFLICT DO
// NOTHING, jadi dijalankan berkali-kali tidak menggandakan data.
//
// Semua kode serial, PIN klaim, PIN kasir, dan token device di sini adalah
// FIXTURE DEMO yang diturunkan secara deterministik dari konstanta publik,
// supaya test dan skrip demo bisa menghitungnya. Nilai ini bukan rahasia.
// Mint produksi (Fase 2) memakai crypto/rand.
package seed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
)

const (
	// demoFixtureKey adalah kunci HMAC publik untuk menurunkan fixture demo.
	// BUKAN rahasia.
	demoFixtureKey = "mganik-demo-fixture-v1"

	// UnitsPerCarton dan CartonsPerPallet mengikuti packaging_specs demo.
	UnitsPerCarton   = 12
	CartonsPerPallet = 4
	// DemoUnitCount = 1 palet x 4 karton x 12 unit.
	DemoUnitCount = UnitsPerCarton * CartonsPerPallet

	// DemoCashierPIN adalah PIN POS kasir demo (6 digit, di-hash argon2id).
	DemoCashierPIN = "135790"
)

// idNamespace memisahkan UUID deterministik seed dari UUID lain.
var idNamespace = uuid.MustParse("6f1d2c3b-4a59-4e8f-9d0c-7b6a5e4d3c2b")

// ID mengembalikan UUID deterministik (v5) untuk nama fixture, misal
// "location:GDP-BKS". Data produksi memakai UUIDv7 dari aplikasi.
func ID(name string) uuid.UUID {
	return uuid.NewSHA1(idNamespace, []byte(name))
}

// demoEpoch: waktu tetap untuk timestamp fixture supaya seed ulang identik.
var demoEpoch = time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)

func derive(label string) []byte {
	mac := hmac.New(sha256.New, []byte(demoFixtureKey))
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

func mustCrockford(b []byte, n int) string {
	s, err := domain.EncodeCrockford(b, n)
	if err != nil {
		panic(err) // panjang input tetap; tidak mungkin gagal
	}
	return s
}

// DemoSerialCode: kode 16 karakter (80 bit pertama HMAC) untuk serial demo
// level UNIT/CARTON/PALLET ke-index (mulai 0).
func DemoSerialCode(level string, index int) string {
	return mustCrockford(derive(fmt.Sprintf("code:%s:%d", level, index))[:10], domain.SerialCodeLen)
}

// DemoClaimPIN: PIN klaim 10 karakter (50 bit pertama HMAC) unit demo ke-index.
func DemoClaimPIN(index int) string {
	return mustCrockford(derive(fmt.Sprintf("pin:UNIT:%d", index))[:7], domain.ClaimPINLen)
}

// DemoSerialID: UUID serial demo.
func DemoSerialID(level string, index int) uuid.UUID {
	return ID(fmt.Sprintf("serial_unit:%s:%d", level, index))
}

// DemoDeviceToken: token bearer device POS demo (256 bit, hex).
func DemoDeviceToken() string {
	return "demo-pos-" + hex.EncodeToString(derive("pos-device:"+StoreJakselCode+":1"))
}

// DemoDeviceTokenHash: yang disimpan di pos_devices.token_hash.
func DemoDeviceTokenHash() []byte {
	sum := sha256.Sum256([]byte(DemoDeviceToken()))
	return sum[:]
}

// DemoDeviceSigningKey: signing key HMAC transaksi device POS demo (32 byte),
// satu-satunya sumber nilai ini. Diturunkan dari demoFixtureKey publik, jadi
// BUKAN rahasia; seed menyimpannya terenkripsi (PII_ENC_KEY) dan `api seed
// --print-demo` mencetaknya dalam hex. Tidak pernah ditulis ke log.
func DemoDeviceSigningKey() []byte {
	return derive("pos-device-signing-key:" + StoreJakselCode + ":1")
}

// DemoDeviceSigningKeyHex: format yang dipakai klien POS (hex huruf kecil).
func DemoDeviceSigningKeyHex() string { return hex.EncodeToString(DemoDeviceSigningKey()) }

// DemoUnit menggambarkan satu unit demo beserta karton induknya.
type DemoUnit struct {
	Index      int
	ID         uuid.UUID
	Code       string
	PIN        string
	CartonCode string
}

// DemoUnits mengembalikan 48 unit demo (12 per karton, urut karton).
func DemoUnits() []DemoUnit {
	units := make([]DemoUnit, DemoUnitCount)
	for i := range units {
		units[i] = DemoUnit{
			Index:      i,
			ID:         DemoSerialID("UNIT", i),
			Code:       DemoSerialCode("UNIT", i),
			PIN:        DemoClaimPIN(i),
			CartonCode: DemoSerialCode("CARTON", i/UnitsPerCarton),
		}
	}
	return units
}

// ------------------------------------------------------------------ kode master data

const (
	BrandCode = "DEMO"
	SKU1      = "DEMO-HRB-SCH10" // serbuk herbal, 10 sachet
	SKU2      = "DEMO-TEH-BAG20" // teh herbal celup, 20 kantong

	CentralWHCode     = "GDP-BKS"
	HubSurabayaCode   = "HUB-SBY"
	StoreJakselCode   = "TKO-JKS"
	StoreBandungCode  = "TKO-BDG"
	PartnerJatimCode  = "MTR-JATIM"
	PartnerSulselCode = "MTR-SULSEL"
	InTransitCode     = "TRANSIT"

	CompanyPartyKey = "party:company"
	JatimPartyKey   = "party:distributor-jatim"
	SulselPartyKey  = "party:distributor-sulsel"

	DemoLotNo = "L2610A"
)

// DemoAccount: akun staff demo. Password semua akun = SEED_DEMO_PASSWORD.
type DemoAccount struct {
	Email     string
	Name      string
	Role      string
	ScopeMode string
	// Locations hanya dipakai kalau ScopeMode = SELECTED.
	Locations []string
}

// DemoAccounts: satu akun per role RBAC.
var DemoAccounts = []DemoAccount{
	{Email: "admin@mganik.test", Name: "Admin Demo", Role: "ADMIN", ScopeMode: "ALL"},
	{Email: "ops@mganik.test", Name: "Manajer Operasional Demo", Role: "OPS_MANAGER", ScopeMode: "ALL"},
	{Email: "produksi@mganik.test", Name: "Operator Produksi Demo", Role: "PRODUCTION_OPERATOR", ScopeMode: "SELECTED", Locations: []string{CentralWHCode}},
	{Email: "gudang@mganik.test", Name: "Staf Gudang Demo", Role: "WAREHOUSE_STAFF", ScopeMode: "SELECTED", Locations: []string{CentralWHCode, HubSurabayaCode}},
	{Email: "kepala.jaksel@mganik.test", Name: "Kepala Toko Jakarta Selatan", Role: "STORE_MANAGER", ScopeMode: "SELECTED", Locations: []string{StoreJakselCode}},
	{Email: "kasir.jaksel@mganik.test", Name: "Kasir Jakarta Selatan", Role: "CASHIER", ScopeMode: "SELECTED", Locations: []string{StoreJakselCode}},
	{Email: "marketing@mganik.test", Name: "Analis Marketing Demo", Role: "MARKETING_ANALYST", ScopeMode: "ALL"},
}

// UserID: UUID deterministik akun demo.
func UserID(email string) uuid.UUID { return ID("user:" + email) }

// LocationID: UUID deterministik lokasi demo.
func LocationID(code string) uuid.UUID { return ID("location:" + code) }

// ProductID: UUID deterministik produk demo.
func ProductID(sku string) uuid.UUID { return ID("product:" + sku) }

// BatchID: UUID batch demo.
func BatchID() uuid.UUID { return ID("batch:" + SKU1 + ":" + DemoLotNo) }

// PosDeviceID: UUID device POS demo di Toko Jakarta Selatan.
func PosDeviceID() uuid.UUID { return ID("pos_device:" + StoreJakselCode + ":1") }
