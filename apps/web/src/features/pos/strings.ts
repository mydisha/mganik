// Teks layar kasir. Bahasa Indonesia, kalimat biasa, singkat. Pesan error dari
// server (detail/message) ditampilkan apa adanya; teks di sini hanya untuk
// keadaan yang diketahui klien sendiri (offline, jaringan, validasi lokal).
import { formatClock, formatNumber, formatRupiah, formatTxnNumber } from "./lib/format";

export const posStrings = {
  title: "Kasir",
  brand: "mGanik Kasir",

  boot: {
    loading: "Membuka data kasir di perangkat ini",
    storageErrorTitle: "Penyimpanan perangkat tidak bisa dibuka",
    storageErrorBody:
      "Kasir butuh penyimpanan browser (IndexedDB) untuk bekerja offline. Mode penyamaran atau pengaturan privasi yang memblokir data situs bisa menjadi penyebabnya.",
    retry: "Coba lagi",
  },

  pwa: {
    updateReady: "Versi baru kasir sudah diunduh.",
    reload: "Muat ulang",
    reloadHint: "Keranjang tetap tersimpan setelah dimuat ulang.",
  },

  setup: {
    title: "Daftarkan perangkat kasir",
    intro:
      "Tempel token dan kunci tanda tangan yang muncul saat kepala toko mendaftarkan perangkat ini di Command Center. Keduanya hanya ditampilkan sekali di sana.",
    tokenLabel: "Token perangkat",
    tokenHint:
      "Salin persis dari Command Center, tanpa spasi. Kalau Anda menempel token dan kunci sekaligus, keduanya diisi otomatis.",
    keyLabel: "Kunci tanda tangan",
    keyHint: "64 karakter heksadesimal (0-9, a-f).",
    tokenFormat: "Token harus 20-200 karakter tanpa spasi. Salin ulang dari Command Center.",
    keyFormat: "Kunci harus 64 karakter 0-9 atau a-f.",
    submit: "Daftarkan perangkat",
    submitting: "Mengunduh data toko",
    network: "Server tidak terjangkau. Pendaftaran pertama butuh koneksi internet; coba lagi setelah perangkat online.",
    outboxNotEmpty:
      "Masih ada transaksi dari pendaftaran sebelumnya di perangkat ini. Ekspor atau keluarkan perangkat dulu sebelum mendaftar ulang.",
    keyStorageTitle: "Di mana kunci ini disimpan",
    keyStorage:
      "Token dan kunci tersimpan di browser perangkat ini (IndexedDB) dan dipakai untuk menandatangani setiap transaksi. Kunci disimpan dalam bentuk yang tidak bisa disalin keluar oleh halaman. Kalau perangkat hilang atau dipindahtangankan, kepala toko mencabutnya dari Command Center; sejak itu token ditolak server.",
    persistDenied:
      "Browser tidak menjamin data kasir aman dari pembersihan otomatis. Pasang kasir sebagai aplikasi atau izinkan penyimpanan permanen di pengaturan situs supaya transaksi offline tidak hilang.",
    persistUnknown: "Browser ini tidak memberi tahu apakah penyimpanan permanen diizinkan.",
    persistGranted: "Penyimpanan permanen diizinkan browser.",
  },

  device: {
    title: "Perangkat ini",
    name: "Nama perangkat",
    store: "Toko",
    storeCode: "Kode toko",
    registered: "Didaftarkan",
    dataUpdated: "Data toko diperbarui",
    never: "belum pernah",
    storage: "Penyimpanan",
    refresh: "Perbarui data toko",
    refreshing: "Memperbarui",
    refreshNeedsOnline: "Butuh koneksi. Data diperbarui otomatis saat perangkat online.",
    export: "Ekspor transaksi tertunda",
    exportHint: (n: number) =>
      n === 0 ? "Tidak ada transaksi tertunda." : `${formatNumber(n)} transaksi belum diterima server.`,
    wipe: "Keluarkan perangkat",
    wipeTitle: "Keluarkan perangkat ini?",
    wipeBody:
      "Semua data kasir di browser ini dihapus: token, kunci, katalog, keranjang, dan riwayat sinkron. Perangkat harus didaftarkan ulang dari Command Center.",
    wipeOutboxWarning: (n: number) =>
      `${formatNumber(n)} transaksi belum diterima server dan ikut terhapus. Ekspor dulu sebagai cadangan.`,
    wipeConfirm: "Hapus data dan keluarkan",
    cancel: "Batal",
    back: "Kembali ke kasir",
  },

  login: {
    title: "Masuk kasir",
    chooseCashier: "Pilih nama Anda",
    pinFor: (name: string) => `PIN untuk ${name}`,
    pinHint: "6 digit. Bisa diketik dengan keyboard.",
    otherCashier: "Pilih kasir lain",
    checking: "Memeriksa PIN",
    wrong: (remaining: number) => `PIN salah. Sisa ${remaining} percobaan sebelum login dikunci.`,
    locked: (until: number, tz?: string) =>
      `Terlalu banyak PIN salah. Login di perangkat ini terkunci sampai ${formatClock(until, tz)}.`,
    hashInvalid: "Data PIN kasir ini tidak bisa dibaca. Minta kepala toko mengatur ulang PIN di Command Center.",
    unknownCashier: "Kasir ini tidak lagi terdaftar di toko. Perbarui data toko.",
    engineUnavailable:
      "Browser ini tidak bisa menjalankan pemeriksa PIN (WebAssembly diblokir). Percobaan ini tidak dihitung salah. Hubungi tim teknis.",
    noCashiersTitle: "Belum ada kasir untuk toko ini",
    noCashiersBody:
      "Kasir muncul di sini setelah punya izin kasir di toko ini dan PIN diatur di Command Center. Setelah itu, perbarui data toko.",
    deviceSettings: "Pengaturan perangkat",
    digit: (d: string) => `Angka ${d}`,
    pinProgress: (n: number) => `${n} dari 6 digit terisi`,
  },

  header: {
    switchCashier: "Ganti kasir",
    lock: "Kunci layar",
    device: "Perangkat",
    cashier: (name: string) => `Kasir: ${name}`,
    soundOn: "Bunyi scan menyala",
    soundOff: "Bunyi scan mati",
    attention: (n: number) => (n > 0 ? `Perlu perhatian (${formatNumber(n)})` : "Riwayat sinkron"),
  },

  sync: {
    online: "Online",
    offline: "Offline",
    syncing: "Menyinkronkan",
    checking: "Memeriksa koneksi",
    revoked: "Perangkat dicabut",
    blocked: "Sinkron tertahan",
    waiting: (n: number) => `${formatNumber(n)} transaksi menunggu`,
    synced: "semua tersinkron",
    lastSync: (at: number | null, tz?: string) =>
      at ? `sinkron terakhir ${formatClock(at, tz)}` : "belum pernah sinkron",
    offlineTooLong: "Offline terlalu lama: transaksi berikutnya ditandai untuk ditinjau",
    revokedBody:
      "Server menolak token perangkat ini. Sinkron berhenti; transaksi yang belum terkirim tetap tersimpan di perangkat. Hubungi kepala toko.",
    recheck: "Periksa lagi",
    blockedFallback: "Server menolak kiriman sinkron tanpa keterangan. Akan dicoba lagi otomatis; hubungi tim teknis kalau berlanjut.",
    waitingServer: "menunggu konfirmasi server",
    missingSeqs: "server menunggu transaksi yang hilang",
  },

  sell: {
    noPriceList:
      "Daftar harga belum ada untuk toko ini. Penjualan belum bisa dicatat sampai daftar harga tersedia dan data toko diperbarui.",
    scanLabel: "Scan atau cari produk",
    scanPlaceholder: "Scan barcode, QR hologram, atau ketik nama/SKU",
    scanHint: "Enter untuk menambah. Panah atas/bawah memilih hasil pencarian.",
    searchResults: "Hasil pencarian",
    noResults: (q: string) => `Tidak ada produk yang cocok dengan "${q}".`,
    cartTitle: "Keranjang",
    cartEmptyTitle: "Keranjang kosong",
    cartEmptyBody: "Scan barcode produk atau QR hologram. Produk tanpa barcode bisa dicari dengan nama atau SKU.",
    items: (lines: number, units: number) => `${formatNumber(lines)} produk · ${formatNumber(units)} unit`,
    subtotal: "Subtotal",
    discount: "Diskon",
    total: "Total",
    pay: "Bayar",
    removeLine: "Hapus baris",
    cancelSale: "Batalkan",
    qty: "Qty",
    setQty: "Ubah qty",
    setDiscount: "Diskon Rp",
    clearEntry: "Hapus angka",
    backspace: "Hapus satu angka",
    entryLabel: "Angka yang diketik",
    selectLineFirst: "Pilih baris di keranjang dulu.",
    typeNumberFirst: "Ketik angka di numpad dulu.",
    pendingSerial: (code: string) =>
      `Serial ${code} belum terdaftar di toko ini. Scan barcode atau pilih produk untuk unit ini.`,
    cancelPendingSerial: "Lepas serial",
    stockLeft: (n: number) => (n > 0 ? `Stok ${formatNumber(n)}` : "Stok tercatat habis"),
    serialCount: (n: number) => `${formatNumber(n)} serial`,
    each: (price: number) => `@ ${formatRupiah(price)}`,
    highDiscount: (pct: number) => `Diskon di atas ${pct}% ditandai untuk ditinjau kepala toko.`,
    confirmRemoveTitle: "Hapus baris ini?",
    confirmRemoveBody: (name: string, qty: number) => `${name}, ${formatNumber(qty)} unit, dikeluarkan dari keranjang.`,
    confirmRemove: "Hapus",
    confirmCancelTitle: "Batalkan transaksi ini?",
    confirmCancelBody: "Semua produk di keranjang dikeluarkan. Belum ada yang tercatat.",
    confirmCancel: "Kosongkan keranjang",
    keep: "Kembali",
  },

  scan: {
    added: (name: string) => `Ditambahkan: ${name}`,
    addedSerial: (name: string, code: string) => `Ditambahkan: ${name} (serial ${code})`,
    soldBefore: (code: string) =>
      `Serial ${code} sudah pernah terjual di perangkat ini. Tetap ditambahkan; server akan mencatatnya sebagai konflik untuk diperiksa.`,
    notFound: (value: string) => `Kode ${value} tidak ada di katalog toko.`,
    noPrice: (name: string) => `${name} belum ada di daftar harga toko. Belum bisa dijual.`,
    invalidUrl: "QR ini bukan QR hologram mGanik yang sah.",
    serialDuplicate: "Serial ini sudah ada di keranjang.",
    serialLimit: (n: number) => `Maksimal ${formatNumber(n)} serial per transaksi.`,
    qtyLimit: "Qty sudah maksimal (1.000) untuk baris ini.",
    lineLimit: "Keranjang sudah 200 baris. Selesaikan transaksi ini dulu.",
  },

  numpadErrors: {
    qty: {
      not_integer: "Qty harus bilangan bulat.",
      below_min: "Qty minimal 1. Untuk mengeluarkan produk, pakai Hapus baris.",
      above_max: "Qty maksimal 1.000 per baris.",
      below_serials: "Qty tidak boleh kurang dari jumlah serial yang sudah dipindai.",
    },
    discount: {
      not_integer: "Diskon harus rupiah utuh.",
      negative: "Diskon tidak boleh negatif.",
      exceeds_subtotal: "Diskon melebihi harga baris ini.",
    },
  },

  payment: {
    title: "Pembayaran",
    totalDue: "Total belanja",
    paid: "Dibayar",
    remaining: "Kurang",
    change: "Kembalian",
    method: "Metode",
    methods: { CASH: "Tunai", EDC: "Kartu (EDC)", QRIS: "QRIS" },
    amount: "Nominal",
    approvalRef: { EDC: "Kode approval EDC", QRIS: "Nomor referensi QRIS" },
    approvalHint: "Salin dari struk mesin EDC atau layar QRIS.",
    add: "Tambahkan pembayaran",
    exact: "Uang pas",
    threeZeros: "Tambah tiga nol",
    list: "Pembayaran tercatat",
    remove: (label: string) => `Hapus pembayaran ${label}`,
    complete: "Selesaikan transaksi",
    completing: "Menyimpan",
    cancel: "Kembali ke keranjang",
    zeroTotal: "Total Rp0: transaksi dicatat tanpa pembayaran dan ditandai untuk ditinjau.",
    valueLimit: (limit: number) => `Total di atas ${formatRupiah(limit)} ditandai untuk ditinjau kepala toko.`,
    errors: {
      amount_invalid: "Nominal harus rupiah utuh, minimal Rp1.",
      approval_ref_required: "Isi kode approval atau nomor referensi dari mesin pembayaran.",
      approval_ref_invalid:
        "Kode approval maksimal 64 byte (huruf dan angka biasa = 1 byte), tanpa baris baru atau karakter kontrol.",
      approval_ref_duplicate: "Kode approval ini sudah dicatat untuk metode yang sama di transaksi ini.",
      cash_change_too_large: "Kembalian lebih dari Rp1.000.000. Periksa lagi nominal tunai yang diterima.",
      scanIgnored: "Isi scan masuk ke kolom nominal dan diabaikan. Ketik nominal dengan numpad atau keyboard.",
      multiple_cash: "Tunai sudah dicatat. Hapus dulu kalau nominalnya salah.",
      non_cash_exceeds_total: "Pembayaran non-tunai tidak boleh melebihi total. Kembalian hanya dari tunai.",
      too_many: "Maksimal 10 pembayaran per transaksi.",
      insufficient: "Pembayaran belum menutup total.",
    },
    saleErrors: {
      no_price_list: "Daftar harga belum ada. Perbarui data toko.",
      empty_cart: "Keranjang kosong.",
      fallback: "Transaksi belum tersimpan. Periksa isi keranjang lalu coba lagi.",
    },
  },

  receipt: {
    title: "Transaksi tersimpan",
    number: (seq: number) => `No. ${formatTxnNumber(seq)}`,
    change: "Kembalian",
    total: "Total",
    paidWith: "Dibayar",
    statusPending: "Tersimpan di perangkat, menunggu sinkron",
    statusSynced: "Diterima server",
    statusConflict: "Diterima server, dengan catatan",
    statusRejected: "Ditolak server, lihat Perlu perhatian",
    next: "Transaksi baru",
  },

  attention: {
    title: "Perlu perhatian dan riwayat sinkron",
    description: "Transaksi yang ditolak server, nomor transaksi yang hilang, dan catatan dari sinkron terakhir.",
    revokedTitle: "Token perangkat ditolak server",
    blockedTitle: "Sinkron tertahan",
    rejectedTitle: "Ditolak server",
    rejectedEmpty: "Tidak ada transaksi yang ditolak.",
    missingTitle: "Nomor yang belum diterima server",
    missingBody: (seqs: string, truncated: boolean) =>
      `Server belum menerima transaksi No. ${seqs}${truncated ? " dan lainnya" : ""}, dan transaksi itu tidak ada di perangkat ini. Laporkan ke tim operasional; toko tetap tercatat offline sampai ini diselesaikan.`,
    recentTitle: "Catatan dari sinkron terbaru",
    recentEmpty: "Belum ada catatan. Konflik stok dan tanda dari server untuk transaksi yang diterima muncul di sini.",
    serverSays: "Alasan dari server",
    whatToDo: "Yang perlu dilakukan",
    resolve: "Tandai sudah ditangani",
    close: "Tutup",
    txnLine: (seq: number, at: number, total: number, tz?: string) =>
      `No. ${formatTxnNumber(seq)} · ${formatClock(at, tz)} · ${formatRupiah(total)}`,
    // Langkah lanjut per alasan penolakan (kode dari kontrak /pos/sync).
    actions: {
      cashier_not_allowed:
        "Penjualan sudah terjadi tetapi kasirnya tidak diizinkan di toko ini. Kepala toko memeriksa izin kasir di Command Center dan mencatat penjualan ini secara manual.",
      invalid_signature:
        "Tanda tangan transaksi tidak cocok dengan kunci perangkat. Kemungkinan kunci yang ditempel saat pendaftaran salah. Ekspor transaksi tertunda dan serahkan ke tim operasional.",
      device_seq_reused:
        "Nomor transaksi bentrok dengan transaksi lain dari perangkat ini. Ekspor transaksi tertunda dan serahkan ke tim operasional.",
      idempotency_key_reused:
        "Transaksi dengan identitas yang sama sudah tercatat dengan isi berbeda. Yang pertama tetap berlaku; cocokkan dengan struk fisik.",
      unknown_product:
        "Produk di transaksi ini tidak ada di katalog server. Kepala toko mencatat penjualan ini secara manual.",
      total_mismatch: "Total transaksi tidak cocok. Kepala toko mencocokkan dengan struk dan mencatatnya manual.",
      payment_insufficient: "Pembayaran tercatat kurang dari total. Kepala toko mencocokkan uang di laci.",
      fallback: "Kepala toko memeriksa transaksi ini dan mencatatnya secara manual bila perlu.",
    } as Record<string, string>,
  },

  conflicts: {
    OFFLINE_SALE_PREEMPT: (qty: number, product: string) =>
      `Penjualan tercatat. ${formatNumber(qty)} unit ${product} diambil dari pesanan online; tim online mengalihkan pesanan itu.`,
    STOCK_DISCREPANCY: (qty: number, product: string) =>
      `Penjualan tercatat, tetapi stok ${product} di sistem kurang ${formatNumber(qty)} unit. Cek stok fisik.`,
    SERIAL_DOUBLE_SALE: (code: string) =>
      `Serial ${code} sudah tercatat terjual atau bukan milik toko ini. Kepala toko memeriksa asal unit ini.`,
    SERIAL_UNREGISTERED: (code: string) =>
      `Serial ${code} tidak terdaftar. Kepala toko memeriksa asal unit ini; kemungkinan produk tidak resmi.`,
    PAYMENT_REF_DUPLICATE: (ref: string) =>
      `Kode approval ${ref} sudah dipakai transaksi lain. Pembayaran dicatat menunggu pengecekan.`,
  },

  flags: {
    PRICE_STALE: "Harga memakai daftar harga lama.",
    PRICE_MISMATCH: "Harga berbeda dari daftar harga yang berlaku.",
    OFFLINE_TOO_LONG: "Diterima setelah offline terlalu lama.",
    CLOCK_SKEW: "Jam perangkat berbeda dari server. Periksa pengaturan jam.",
    FUTURE_TIMESTAMP: "Jam perangkat lebih maju dari server. Periksa pengaturan jam.",
    HIGH_DISCOUNT: "Diskon tinggi, ditinjau kepala toko.",
    OFFLINE_VALUE_LIMIT: "Nilai transaksi offline di atas batas, ditinjau kepala toko.",
    ZERO_TOTAL: "Transaksi Rp0.",
    CASHIER_INACTIVE: "Akun kasir sudah nonaktif saat sinkron.",
    SERIAL_INVALID_FORMAT: "Ada kode serial yang formatnya tidak sah.",
  } as Record<string, string>,

  unknownProduct: "produk ini",
} as const;
