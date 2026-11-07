export const authStrings = {
  login: {
    title: "Masuk ke Command Center",
    description: "Pakai email dan password akun staff mGanik Anda.",
    email: "Email",
    password: "Password",
    submit: "Masuk",
    submitting: "Memeriksa…",
    emailRequired: "Isi email Anda.",
    emailInvalid: "Format email belum benar, contoh: nama@mganik.id.",
    passwordRequired: "Isi password Anda.",
    sessionExpired: "Sesi Anda sudah berakhir. Masuk lagi untuk melanjutkan.",
    tooManyAttempts: (minutes: number) => `Terlalu banyak percobaan masuk. Coba lagi dalam ${minutes} menit.`,
    checking: "Memeriksa sesi…",
  },
  session: {
    loadFailed: "Profil akun tidak bisa dimuat",
  },
  account: {
    menu: "Menu akun",
    signOut: "Keluar",
    signingOut: "Keluar…",
    signedOut: "Anda sudah keluar.",
  },
} as const;
