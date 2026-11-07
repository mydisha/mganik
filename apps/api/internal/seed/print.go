package seed

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// PrintDemo menulis daftar fixture demo (kode serial, PIN klaim, akun, token
// device) ke w. Hanya dipanggil atas permintaan eksplisit (`api seed
// --print-demo`); nilai ini tidak pernah ditulis ke log.
func PrintDemo(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	p := func(format string, a ...any) { fmt.Fprintf(tw, format, a...) }

	p("# Data demo mGanik (fixture deterministik, BUKAN rahasia produksi)\n\n")
	p("Batch\t%s / lot %s\n", SKU1, DemoLotNo)
	p("Palet\t%s\n", DemoSerialCode("PALLET", 0))
	for c := range CartonsPerPallet {
		p("Karton %d\t%s\n", c+1, DemoSerialCode("CARTON", c))
	}
	p("\n#\tKODE UNIT\tPIN KLAIM\tKARTON\n")
	for _, u := range DemoUnits() {
		p("%d\t%s\t%s\t%s\n", u.Index+1, u.Code, u.PIN, u.CartonCode)
	}

	p("\nAKUN\tROLE\tSCOPE\n")
	for _, a := range DemoAccounts {
		scope := a.ScopeMode
		if len(a.Locations) > 0 {
			scope = fmt.Sprintf("%s %v", a.ScopeMode, a.Locations)
		}
		p("%s\t%s\t%s\n", a.Email, a.Role, scope)
	}
	p("\nPassword semua akun\tnilai SEED_DEMO_PASSWORD saat seed pertama kali jalan\n")
	p("PIN POS kasir\t%s\n", DemoCashierPIN)
	p("Token device POS (%s)\t%s\n", StoreJakselCode, DemoDeviceToken())
	p("Signing key device POS (%s, hex)\t%s\n", StoreJakselCode, DemoDeviceSigningKeyHex())
	return tw.Flush()
}
