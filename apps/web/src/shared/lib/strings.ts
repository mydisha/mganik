// Teks UI lintas fitur. Teks milik satu fitur ada di features/<fitur>/strings.ts.
export const strings = {
  brand: "mGanik",
  common: {
    retry: "Coba lagi",
    loadMore: "Muat lebih banyak",
    loadingMore: "Memuat…",
    loadMoreFailed: "Halaman berikutnya gagal dimuat.",
    cancel: "Batal",
    close: "Tutup",
    clearFilters: "Hapus semua filter",
    removeFilter: (label: string) => `Hapus filter ${label}`,
    emptyValue: "tidak ada",
    copy: (label: string) => `Salin ${label}`,
    copied: (label: string) => `${label} disalin.`,
    copyFailed: "Tidak bisa menyalin otomatis. Pilih teksnya lalu salin manual.",
    requestId: "Kode referensi",
  },
  errors: {
    network: "Tidak bisa terhubung ke server. Periksa koneksi internet, lalu coba lagi.",
    timeout: "Server terlalu lama menjawab. Coba lagi sebentar lagi.",
    unknown: "Terjadi kesalahan yang tidak terduga. Coba lagi beberapa saat lagi.",
    loadTitle: "Data tidak bisa dimuat",
    forbiddenTitle: "Anda tidak punya akses ke halaman ini",
    forbiddenDescription: "Peran Anda belum mencakup izin untuk bagian ini. Minta admin menambahkan izinnya.",
  },
  pos: {
    title: "Kasir",
    description: "Transaksi tetap berjalan saat internet mati, lalu tersinkron otomatis.",
    emptyTitle: "Perangkat kasir belum terdaftar",
    emptyDescription: "Daftarkan perangkat ini ke toko lewat Command Center sebelum mulai berjualan.",
  },
} as const;
