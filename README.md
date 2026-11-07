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
| `migrate` | Menerapkan migrasi lalu keluar | — |
| `api` | Backend Go (Gin) | http://localhost:8089 |
| `web` | Build React disajikan nginx, mem-proxy `/api` ke `api` | http://localhost:8088 |

Halaman: `/console` (Command Center), `/pos` (kasir), `/v/<kode>` (verifikasi konsumen).
Port bisa diubah lewat `.env` (salin dari `.env.example`). `make up` menjalankan hal yang
sama di background; `make down` mematikannya, `make clean` juga menghapus data database.

## Development

Prasyarat tambahan: Go 1.27, Node 22.12+, pnpm 12, opsional [air](https://github.com/air-verse/air) untuk hot reload.

```bash
make hooks     # sekali per clone
make db        # Postgres saja
make migrate
make api       # backend di :8089 (matikan stack compose dulu)
make web       # Vite di :5173, proxy ke :8089
```

| Perintah | Fungsi |
|---|---|
| `make test` | Test Go (`-race`) dan frontend |
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
