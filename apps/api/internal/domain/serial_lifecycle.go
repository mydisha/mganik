package domain

import "sort"

// Kode status serial unit (isi serial_unit_statuses). Kode membaca atribut
// status dari tabel; konstanta ini hanya untuk status yang punya arti khusus
// di alur (mint, commission, kirim, terima, jual, klaim, recall).
const (
	SerialGenerated         = "GENERATED"
	SerialCommissioned      = "COMMISSIONED"
	SerialInStock           = "IN_STOCK"
	SerialInTransit         = "IN_TRANSIT"
	SerialAtPartner         = "AT_PARTNER"
	SerialSold              = "SOLD"
	SerialConsumerActivated = "CONSUMER_ACTIVATED"
	SerialReturned          = "RETURNED"
	SerialQuarantined       = "QUARANTINED"
	SerialDecommissioned    = "DECOMMISSIONED"
)

// Level kemasan yang dipakai alur (isi packaging_levels).
const (
	LevelUnit   = "UNIT"
	LevelCarton = "CARTON"
	LevelPallet = "PALLET"
)

// LifecycleTransition adalah satu baris lifecycle_transitions untuk serial unit.
// From kosong = status awal saat entitas dibuat. Permission kosong = transisi
// hanya dipicu sistem, konsumen, atau pihak luar.
type LifecycleTransition struct {
	From       string
	To         string
	BizStep    string
	Permission string
}

// Lifecycle adalah state machine serial unit yang dibangun dari data
// lifecycle_transitions. Aman dibaca bersamaan setelah dibuat.
type Lifecycle struct {
	byPair      map[[2]string]LifecycleTransition
	allowedFrom map[string][]string
}

// NewLifecycle membangun state machine dari daftar transisi.
func NewLifecycle(ts []LifecycleTransition) *Lifecycle {
	l := &Lifecycle{
		byPair:      make(map[[2]string]LifecycleTransition, len(ts)),
		allowedFrom: make(map[string][]string),
	}
	for _, t := range ts {
		l.byPair[[2]string{t.From, t.To}] = t
		if t.From != "" {
			l.allowedFrom[t.To] = append(l.allowedFrom[t.To], t.From)
		}
	}
	for to := range l.allowedFrom {
		sort.Strings(l.allowedFrom[to])
	}
	return l
}

// Lookup mengembalikan transisi from → to kalau ada di data.
func (l *Lifecycle) Lookup(from, to string) (LifecycleTransition, bool) {
	t, ok := l.byPair[[2]string{from, to}]
	return t, ok
}

// CanTransition melaporkan apakah from → to adalah transisi yang sah.
// Self-loop tidak pernah sah (DB juga menolaknya).
func (l *Lifecycle) CanTransition(from, to string) bool {
	if from == to {
		return false
	}
	_, ok := l.Lookup(from, to)
	return ok
}

// AllowedFrom mengembalikan salinan daftar status asal yang sah untuk tujuan
// to, terurut. Dipakai sebagai `WHERE status = ANY(...)` di conditional update.
func (l *Lifecycle) AllowedFrom(to string) []string {
	return append([]string(nil), l.allowedFrom[to]...)
}
