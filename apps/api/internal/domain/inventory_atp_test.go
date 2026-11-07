package domain

import "testing"

func TestOnlineATP(t *testing.T) {
	tests := []struct {
		name      string
		available int64
		buffer    int64
		sync      string
		want      int64
	}{
		{"gudang tanpa buffer", 10, 0, SyncStateNotTracked, 10},
		{"toko online dikurangi buffer", 5, 2, SyncStateOnline, 3},
		{"buffer sama dengan available", 2, 2, SyncStateOnline, 0},
		{"buffer melebihi available tidak negatif", 1, 2, SyncStateOnline, 0},
		{"toko offline selalu 0", 100, 0, SyncStateOffline, 0},
		{"available 0", 0, 0, SyncStateOnline, 0},
		{"buffer negatif diabaikan", 4, -3, SyncStateOnline, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OnlineATP(tt.available, tt.buffer, tt.sync); got != tt.want {
				t.Fatalf("OnlineATP(%d, %d, %s) = %d, ingin %d", tt.available, tt.buffer, tt.sync, got, tt.want)
			}
		})
	}
}
