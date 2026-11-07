# Desain antarmuka mGanik

Tiga permukaan, satu sistem token:

| Permukaan | Route | Pengguna | Karakter |
|---|---|---|---|
| Command Center | `/console` | Ops, gudang, produksi, marketing | Tenang dan padat seperti dashboard operasional; tabel, filter, antrean kerja |
| Kasir | `/pos` | Kasir toko | Offline-first, target sentuh besar, bisa dipakai penuh dengan keyboard + scanner |
| Verifikasi | `/v/:code` | Konsumen dari QR hologram | Mobile-first (360 px), tanpa login, satu hasil yang jelas |

Kode ada di `apps/web/src`. Token di `app/styles.css`, komponen bersama di `shared/ui`,
semua teks UI di `shared/lib/strings.ts`. Kalau sebuah fitur butuh sesuatu yang tidak ada
di sini, tambahkan dulu di dokumen ini.

## Stack

React 19, Vite, TypeScript strict, Tailwind v4, shadcn/ui (new-york, Radix, CSS variable),
lucide, IBM Plex Sans dan IBM Plex Mono (self-hosted lewat `@fontsource`), react-router 7,
TanStack Query, openapi-fetch dengan tipe dari `packages/contracts/openapi.yaml`.

## Token

Semua warna adalah CSS variable `oklch`, didefinisikan untuk terang (`:root`) dan gelap
(`.dark`), lalu diekspos ke Tailwind lewat `@theme inline`. Komponen memakai utilitas token
(`bg-card`, `text-muted-foreground`, `border-danger-border`), tidak pernah hex, rgb, atau
palet bawaan Tailwind seperti `text-red-600`.

| Token | Dipakai untuk |
|---|---|
| `background` / `foreground` | Latar halaman dan teks utama |
| `card` / `card-foreground` | Tabel, kartu, input |
| `popover` / `popover-foreground` | Dialog, menu, tooltip |
| `primary` / `primary-foreground` | Satu aksi utama per tampilan |
| `secondary`, `accent`, `muted` (+ `-foreground`) | Hover, permukaan tenang, teks bantu. `muted-foreground` adalah teks paling terang yang diizinkan |
| `destructive` / `destructive-foreground` | Aksi yang membatalkan, mencabut, atau menghapus |
| `border`, `input`, `ring` | Pemisah, border kontrol (3:1 terhadap `card`), cincin fokus |
| `link` | Tautan di dalam teks |
| `overlay` | Lapisan gelap di belakang dialog dan sheet |

Nada status (latar tipis + teks + border):

| Nada | Dipakai untuk |
|---|---|
| `success` | Siap, tersinkron, asli, terjual, diterima |
| `warning` | Menunggu, offline dengan antrean sync, stok menipis, perlu review |
| `danger` | Gagal, mencurigakan, konflik yang butuh tindakan, stok kritis |
| `info` | Dalam proses, dalam perjalanan |
| `neutral` | Tidak aktif, belum dicek, status tidak dikenal |

Setiap pasangan teks/latar minimal 4.5:1 di kedua tema; border kontrol dan cincin fokus
minimal 3:1. Cek ulang kontras setiap kali token diubah.

### Tipografi

- Sans: IBM Plex Sans 400/500/600. Mono: IBM Plex Mono 400/500 untuk kode serial, id, SKU, dan JSON.
- Ukuran dasar 14 px (`text-sm`). Input memakai 16 px di bawah 768 px supaya iOS tidak zoom.
- Skala: judul halaman `text-xl font-semibold` (satu `h1`), judul bagian `text-base font-semibold` (`h2`), isi `text-sm`, teks bantu dan header tabel `text-xs`. Tidak ada teks di bawah 12 px.
- Angka yang dibandingkan (rupiah, qty, DOI) memakai `tabular-nums`.

### Jarak, radius, bayangan

- Grid 4 px. Gutter halaman 16 px di mobile, 24 px mulai 768 px. Bagian ditumpuk dengan `gap-6`, kontrol di dalam bagian `gap-3`.
- Radius dari `--radius` (8 px): `rounded-md` kontrol, `rounded-lg` kartu dan tabel, `rounded-full` badge.
- Bayangan datar: `shadow-xs` kartu dan kontrol, `shadow-md` menu, `shadow-lg` dialog.

## Komponen

| Komponen | Pakai untuk |
|---|---|
| `Button` | Semua aksi. Varian `default` (satu per tampilan), `outline`, `ghost`, `secondary`, `destructive`, `link`. Semua ukuran mencapai 44 px di bawah 768 px |
| `StatusBadge` | Setiap status. Selalu berupa teks; warna hanya penguat |
| `PageHeader` | `h1`, satu kalimat konteks, aksi halaman |
| `EmptyState` | Belum ada data: apa yang akan tampil dan langkah berikutnya |

## Command Center

- Ringkasan ditulis sebagai kalimat sebelum tabel ("3 SKU akan habis dalam 5 hari di 2 toko").
- Maksimal tiga blok per halaman. Setiap fakta tampil sekali.
- Tabel: baris 40 px, header `text-xs text-muted-foreground`, angka rata kanan. Di bawah 768 px baris menjadi kartu; halaman tidak pernah menggulung ke samping.

## Kasir

- Target sentuh minimal 48 px; tombol aksi utama 56–64 px; numpad 64 × 64 px.
- Kolom scan selalu mendapat fokus kembali setelah dialog ditutup.
- Status sinkronisasi selalu terlihat: ikon + teks ("Offline · 12 transaksi menunggu"), tidak hanya warna.
- Pintasan keyboard ditampilkan di tombolnya (F2 bayar, F4 hapus baris, Esc batal).
- Umpan balik scan: bunyi pendek berbeda untuk berhasil dan gagal, ditambah sorotan baris.
- Target layar: 1024 × 768 lanskap dan 1366 × 768.

## Verifikasi konsumen

- Satu hasil besar dan jelas di atas: asli, pernah diverifikasi (dengan tanggal dan kota), mencurigakan, tidak terdaftar, atau ditarik.
- Status internal tidak pernah ditampilkan ke konsumen.
- Izin lokasi diminta dengan penjelasan satu kalimat; kalau ditolak, verifikasi tetap jalan.

## Format

- Rupiah: bilangan bulat, `Rp150.000` (tanpa spasi, titik ribuan), `Rp0` untuk nol.
- Angka: pengelompokan `id-ID` (`1.234`).
- Waktu: disimpan UTC, ditampilkan di zona lokasi (WIB/WITA/WIT) dengan format `id-ID` dan akhiran zona.
- Kode serial dan id: mono, bisa disalin.
- Nilai kosong: `—`.

## Teks

- Bahasa Indonesia, kalimat biasa (bukan Title Case), singkat dan spesifik.
- Katakan apa yang terjadi dan langkah berikutnya: "Sinkronisasi gagal. Transaksi aman di perangkat; akan dicoba lagi otomatis."

## State

Setiap layar punya state loading (skeleton seukuran isi), kosong, error (dengan "Coba lagi"),
dan, untuk kasir, offline dan konflik.

## Mode gelap

Class `dark` di `<html>` mengganti semua token; komponen tidak butuh varian `dark:`.
Tema mengikuti sistem dan dipasang sebelum render pertama. Cek setiap layar baru di kedua tema.

## Aksesibilitas

- Setiap input punya label terlihat; tombol ikon punya `aria-label`.
- Fokus selalu terlihat (`focus-visible` dari token `ring`).
- Status tidak pernah hanya warna.
- Satu `h1` per halaman, bagian memakai `h2`.
