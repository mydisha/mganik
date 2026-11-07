package domain

// Status sinkron lokasi (locations.sync_state).
const (
	SyncStateNotTracked = "NOT_TRACKED"
	SyncStateOnline     = "ONLINE"
	SyncStateOffline    = "OFFLINE"
)

// OnlineATP menghitung stok yang boleh dijanjikan ke channel online untuk satu
// (lokasi, produk) (PLANNING §7.4 lapis pencegahan):
//
//   - lokasi yang POS-nya OFFLINE tidak diekspos ke online sama sekali (0),
//     karena penjualan kasir yang belum tersinkron tidak terlihat server;
//   - selain itu available dikurangi online_buffer_qty (unit terakhir
//     disisihkan untuk pembeli walk-in), tidak pernah negatif.
func OnlineATP(available, onlineBuffer int64, syncState string) int64 {
	if syncState == SyncStateOffline {
		return 0
	}
	if onlineBuffer < 0 {
		onlineBuffer = 0
	}
	atp := available - onlineBuffer
	if atp < 0 {
		return 0
	}
	return atp
}
