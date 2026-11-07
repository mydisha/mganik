package worker

import (
	"context"

	"mganik/api/internal/service"
)

// JobPosMarkOffline: nama job penanda lokasi POS OFFLINE.
const JobPosMarkOffline = "pos.mark_offline"

// RegisterPOSJobs mendaftarkan job POS ke runner: lokasi tanpa heartbeat
// lebih dari POS_HEARTBEAT_TIMEOUT ditandai OFFLINE setiap putaran worker
// (satu lokasi per transaksi, SKIP LOCKED; aman beberapa instance).
func RegisterPOSJobs(r *Runner, pos *service.PosService) {
	r.RegisterJob(JobPosMarkOffline, func(ctx context.Context) error {
		_, err := pos.MarkStaleLocationsOffline(ctx)
		return err
	})
}
