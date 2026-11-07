// Semua teks UI berbahasa Indonesia dikumpulkan di sini.
export const strings = {
  brand: "mGanik",
  nav: {
    label: "Navigasi utama",
    console: "Command Center",
    pos: "Kasir",
  },
  console: {
    title: "Command Center",
    description: "Visibilitas stok, peringatan runout, dan anomali distribusi di semua lokasi.",
    emptyTitle: "Belum ada data stok",
    emptyDescription: "Ringkasan stok per lokasi, perkiraan habis stok, dan antrean anomali akan tampil di sini.",
  },
  pos: {
    title: "Kasir",
    description: "Transaksi tetap berjalan saat internet mati, lalu tersinkron otomatis.",
    emptyTitle: "Perangkat kasir belum terdaftar",
    emptyDescription: "Daftarkan perangkat ini ke toko lewat Command Center sebelum mulai berjualan.",
  },
  verify: {
    title: "Cek keaslian produk",
    codeLabel: "Kode produk",
    pending: "Hasil verifikasi akan tampil di sini.",
  },
  apiStatus: {
    label: "Status API",
    checking: "Memeriksa…",
    ready: (version: number) => `Terhubung · skema v${version}`,
    notReady: "API belum siap",
    retry: "Coba lagi",
  },
} as const;
