// Package postest berisi test integrasi POS offline-first (Fase 4):
// provisioning device, bootstrap, heartbeat + job penanda OFFLINE, sync
// transaksi (idempotensi, signature, konflik Kasus 4, serial, pembayaran),
// dan resolusi konflik alokasi. Semua test berjalan terhadap PostgreSQL
// sungguhan (testutil) di atas data seed demo.
package postest
