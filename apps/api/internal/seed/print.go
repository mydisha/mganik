package seed

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// PrintDemo menulis daftar fixture demo (kode serial, PIN klaim, akun, token
// device) ke w. Hanya dipanggil atas permintaan eksplisit (`api seed
// --print-demo`); nilai ini tidak pernah ditulis ke log.
func PrintDemo(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	p := func(format string, a ...any) { fmt.Fprintf(tw, format, a...) }

	p("# mGanik demo data\n")
	p("# Fixed values for local demos, identical on every seed. Not production secrets.\n\n")
	p("Batch\t%s / lot %s\n", SKU1, DemoLotNo)
	p("Pallet\t%s\n", DemoSerialCode("PALLET", 0))
	for c := range CartonsPerPallet {
		p("Carton %d\t%s\n", c+1, DemoSerialCode("CARTON", c))
	}
	p("\nNo.\tUnit code\tClaim PIN\tCarton\n")
	for _, u := range DemoUnits() {
		p("%d\t%s\t%s\t%s\n", u.Index+1, u.Code, u.PIN, u.CartonCode)
	}

	p("\nAccount\tRole\tLocations\n")
	for _, a := range DemoAccounts {
		scope := a.ScopeMode
		switch {
		case len(a.Locations) > 0:
			scope = strings.Join(a.Locations, ", ")
		case a.ScopeMode == "ALL":
			scope = "all locations"
		}
		p("%s\t%s\t%s\n", a.Email, a.Role, scope)
	}
	p("\nPassword (all accounts)\tvalue of SEED_DEMO_PASSWORD on the first seed run\n")
	p("Cashier PIN (POS)\t%s\n", DemoCashierPIN)
	p("POS device token (%s)\t%s\n", StoreJakselCode, DemoDeviceToken())
	p("POS device signing key (%s, hex)\t%s\n", StoreJakselCode, DemoDeviceSigningKeyHex())
	return tw.Flush()
}
