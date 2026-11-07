import { formatNumber as n } from "@/shared/lib/format";

// Teks untuk konsumen: kalimat pendek, tanpa istilah internal (status serial,
// aturan radar, atau kode error).
export const verifyStrings = {
  title: "Cek keaslian produk",
  codeLabel: "Kode di hologram",
  locationAsk: "Boleh kami pakai lokasi Anda? Lokasi membantu kami menemukan produk palsu yang beredar di daerah Anda; tanpa lokasi pun produk tetap bisa dicek.",
  checkWithLocation: "Cek dengan lokasi",
  checkWithoutLocation: "Cek tanpa lokasi",
  locating: "Menunggu izin lokasi…",
  checking: "Memeriksa kode…",
  locationDenied: "Lokasi tidak dipakai karena izinnya tidak diberikan. Hasil di atas tetap berlaku.",
  failedTitle: "Kode belum bisa dicek",
  retry: "Coba cek lagi",

  product: {
    name: "Produk",
    lot: "Lot",
    expiry: "Kedaluwarsa",
  },
  results: {
    GENUINE_FIRST: {
      title: "Produk asli",
      body: "Kode ini terdaftar di mGanik dan baru pertama kali dicek.",
    },
    GENUINE_REPEAT: {
      title: "Produk asli, kode ini pernah dicek",
      first: (date: string, city: string | undefined) =>
        city ? `Pertama kali dicek pada ${date} di ${city}.` : `Pertama kali dicek pada ${date}.`,
      firstUnknown: "Kode ini sudah pernah dicek sebelumnya.",
      meaning:
        "Kalau yang mengecek waktu itu Anda atau orang serumah, tidak ada yang perlu dilakukan. Kalau bukan, pastikan segel kemasan masih utuh sebelum dikonsumsi.",
    },
    SUSPICIOUS: {
      title: "Produk ini perlu dicek lebih lanjut",
      body: "Pola pengecekan kode ini tidak wajar, jadi kami belum bisa memastikan keasliannya.",
      steps: [
        "Jangan dikonsumsi dulu.",
        "Simpan kemasan dan struk pembelian.",
        "Hubungi layanan pelanggan mGanik dan sebutkan kode di hologram.",
      ],
    },
    NOT_FOUND: {
      title: "Kode tidak terdaftar",
      body: "Kode ini tidak ada di data produk mGanik.",
      steps: [
        "Pastikan kode di alamat halaman ini sama dengan kode di hologram.",
        "Kalau sama, jangan dikonsumsi dan laporkan tempat Anda membelinya ke layanan pelanggan mGanik.",
      ],
    },
    RECALLED: {
      title: "Produk ini sedang ditarik",
      body: (lot: string | null) =>
        lot ? `mGanik menarik lot ${lot} dari peredaran.` : "mGanik menarik produk dari lot ini dari peredaran.",
      steps: ["Jangan dikonsumsi.", "Kembalikan ke tempat pembelian atau hubungi layanan pelanggan mGanik."],
    },
    stepsHeading: "Yang perlu Anda lakukan",
  },

  claim: {
    title: "Klaim poin",
    description: "Poin diklaim dengan PIN di bagian dalam kemasan, jadi hanya pembeli yang bisa mengklaimnya.",
    phone: "Nomor HP",
    phoneHint: "Nomor Indonesia, contoh 0812 3456 7890. Kami kirim kode masuk ke nomor ini.",
    phoneInvalid: "Nomor HP belum benar. Pakai nomor Indonesia yang diawali 08, 628, atau +628.",
    sendCode: "Kirim kode",
    sendingCode: "Mengirim…",
    codeSent: (phone: string, minutes: number) => `Kode dikirim ke ${phone}. Berlaku ${n(minutes)} menit.`,
    otp: "Kode masuk",
    otpHint: "6 angka dari SMS.",
    otpInvalid: "Kode masuk terdiri dari 6 angka.",
    verify: "Masuk",
    verifying: "Memeriksa…",
    changePhone: "Ganti nomor",
    resend: "Kirim ulang kode",
    signedInAs: (phone: string) => `Masuk sebagai ${phone}.`,
    signOut: "Keluar",
    balance: (available: number, pending: number) =>
      pending > 0
        ? `Saldo Anda ${n(available)} poin, ditambah ${n(pending)} poin yang masih tertahan.`
        : `Saldo Anda ${n(available)} poin.`,
    balanceLoading: "Memuat saldo…",
    balanceFailed: "Saldo belum bisa dimuat.",
    pin: "PIN di dalam kemasan",
    pinHint: "10 karakter huruf dan angka. Spasi atau tanda hubung boleh.",
    pinInvalid: "PIN terdiri dari 10 karakter huruf dan angka.",
    submit: "Klaim poin",
    submitting: "Mengklaim…",
    replayed: "Klaim ini sudah tercatat sebelumnya.",
    approved: (points: number) => `${n(points)} poin masuk ke saldo Anda.`,
    pendingUntil: (points: number, date: string) => `${n(points)} poin tercatat dan bisa dipakai mulai ${date}.`,
    review: (points: number) => `Klaim Anda sedang diperiksa tim kami. ${n(points)} poin masuk setelah klaim disetujui.`,
    rejected: "Klaim ini ditolak. Hubungi layanan pelanggan mGanik kalau menurut Anda keputusan ini keliru.",
  },
} as const;
