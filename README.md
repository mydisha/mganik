# mGanik Omnichannel & Traceability

Prototype fondasi sistem dari lini produksi sampai konsumen: serialisasi hologram dan
anti-counterfeit, stok dan order omnichannel dengan proteksi konkurensi, POS offline-first,
dan Supply Chain Command Center.

## Menjalankan

Prasyarat: Docker dengan Compose v2.

```bash
docker compose up --build
```

Satu perintah itu menjalankan seluruh stack secara berurutan:

| Service | Isi | Alamat |
|---|---|---|
| `db` | PostgreSQL 17 | `localhost:55432` (user/db `mganik`) |
| `migrate` | Menerapkan migrasi, mengisi data demo, lalu keluar | — |
| `api` | Backend Go (Gin) | http://localhost:8089 |
| `web` | Build React disajikan nginx, mem-proxy `/api` ke `api` | http://localhost:8088 |

Halaman: `/console` (Command Center), `/pos` (kasir), `/v/<kode>` (verifikasi konsumen).
Port bisa diubah lewat `.env` (salin dari `.env.example`). `make up` menjalankan hal yang
sama di background; `make down` mematikannya, `make clean` juga menghapus data database.

## Data demo

Service `migrate` menjalankan `api migrate --seed`: migrasi lalu data demo. Seed idempotent
(aman dijalankan ulang) dan hanya jalan kalau `SEED_DEMO=true` (default di compose; set
`SEED_DEMO=false` di `.env` untuk migrasi saja). Isinya: 2 SKU, gudang pusat Bekasi, hub
Surabaya, toko Jakarta Selatan dan Bandung, gudang Distributor Jatim dan Sulsel beserta
wilayah resminya, 1 batch berisi 1 palet → 4 karton → 48 unit serial, saldo stok + ledger,
harga POS dan D2C, satu akun per role, dan 1 device POS di toko Jakarta Selatan.

| Akun | Role | Scope lokasi |
|---|---|---|
| `admin@mganik.test` | ADMIN | semua |
| `ops@mganik.test` | OPS_MANAGER | semua |
| `produksi@mganik.test` | PRODUCTION_OPERATOR | Gudang Pusat |
| `gudang@mganik.test` | WAREHOUSE_STAFF | Gudang Pusat, Hub Surabaya |
| `kepala.jaksel@mganik.test` | STORE_MANAGER | Toko Jakarta Selatan |
| `kasir.jaksel@mganik.test` | CASHIER | Toko Jakarta Selatan |
| `marketing@mganik.test` | MARKETING_ANALYST | semua |

Password semua akun: nilai `SEED_DEMO_PASSWORD`, default **`mganik-demo-2026`**. Ganti di
`.env` sebelum seed pertama kalau stack dibuka ke jaringan lain.

Login staff: `POST /api/v1/auth/login` memasang cookie `__Host-session` (idle 8 jam,
absolut 12 jam); 5 kali gagal per email atau 20 per IP dalam 15 menit memblok sementara.
Login konsumen (klaim poin) memakai OTP mock: minta kode lewat
`POST /api/v1/public/otp/request` dengan nomor seluler Indonesia mana pun, lalu masukkan kode
tetap `OTP_MOCK_CODE`, default **`246810`**. Belum ada pengirim SMS; OTP mock ditolak di
production kecuali `SEED_DEMO_ALLOW_PRODUCTION=true`. Nomor HP disimpan terenkripsi
(`PII_ENC_KEY`) dan tidak pernah ditulis ke log.

Cookie sesi memakai prefix `__Host-` dan atribut `Secure`. Di `http://localhost` (tanpa
TLS) cookie ini diterima Chrome dan Firefox karena localhost dianggap konteks aman, tetapi
**tidak oleh Safari**: pakai Chrome/Firefox untuk demo lokal, atau pasang TLS.

IP klien untuk rate limit dan audit diambil dari `X-Forwarded-For` hanya kalau request
datang dari nginx (container `web`, IP statis `COMPOSE_WEB_IP`, default `172.31.250.10` di
subnet `COMPOSE_SUBNET`); nginx menimpa header itu dengan IP peer. Request langsung ke port
API memakai IP peer-nya sendiri. Kalau subnet bentrok di host, ubah `COMPOSE_SUBNET`,
`COMPOSE_WEB_IP`, dan `COMPOSE_IP_RANGE` bersama di `.env` (lihat `.env.example`).

Compose adalah stack demo lokal: walau `APP_ENV=production`, ia memakai pepper dan akun
demo yang nilainya publik di repo, lewat izin `SEED_DEMO_ALLOW_PRODUCTION=true` (api dan
migrate menulis WARN saat start). Untuk deployment sungguhan set izin itu `false`; config
lalu menolak `SEED_DEMO=true`, `CLAIM_PEPPER`/`PII_ENC_KEY` demo, OTP mock, dan password demo default. `make
migrate/seed/api` membaca `.env` yang sama dan memakai default `CLAIM_PEPPER` yang sama
dengan compose, jadi PIN demo tetap terverifikasi di kedua cara menjalankan. Kalau pepper
diganti, `api seed` berikutnya memperbarui hash PIN fixture demo.

Reset data demo: `make clean` lalu `make up` (menghapus volume database proyek ini). Jangan
memakai goose down: migrasi data lookup sengaja gagal turun selama masih ada data yang
mereferensikannya.

Kode serial, PIN klaim, PIN POS kasir, dan token device POS adalah fixture deterministik
(bukan rahasia) dan tidak pernah ditulis ke log. Untuk melihatnya:

```bash
docker compose run --rm --no-deps migrate seed --print-demo   # lewat image
make demo-data                                                # dari source (butuh Go)
```

## Development

Prasyarat tambahan: Go 1.27, Node 22.12+, pnpm 12, opsional [air](https://github.com/air-verse/air) untuk hot reload.

```bash
make hooks     # sekali per clone
make db        # Postgres saja
make migrate
make seed      # data demo (idempotent)
make api       # backend di :8089 (matikan stack compose dulu)
make web       # Vite di :5173, proxy ke :8089
```

| Perintah | Fungsi |
|---|---|
| `make test` | Test Go (`-race`) dan frontend. Test DB memakai Postgres 17 via testcontainers (butuh Docker), atau server sendiri lewat `TEST_DATABASE_URL` |
| `make lint` | gofmt, go vet, typecheck |
| `make gen` | Tipe TypeScript dari `packages/contracts/openapi.yaml` |

## Struktur

```
apps/api        Go: controller → service → repository (Gin, GORM, goose)
apps/web        React + Vite + Tailwind + shadcn/ui
packages/contracts/openapi.yaml   kontrak API, sumber tipe frontend
compose.yaml    seluruh stack
DESIGN.md       token dan aturan UI
```

Backend memakai pola layered di `apps/api/internal`:

| Package | Tanggung jawab |
|---|---|
| `controller` | Handler Gin: bind dan validasi DTO, panggil service, tulis respons atau problem+json |
| `service` | Use case, batas transaksi, aturan bisnis |
| `repository` | Akses database |
| `dto` | Bentuk request/response (model GORM tidak pernah di-bind dari request) |
| `apperror` | Error domain → `application/problem+json` (RFC 9457) |
| `middleware`, `router`, `config`, `database` | Infrastruktur |
