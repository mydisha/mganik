import "fake-indexeddb/auto";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { defaultSyncMeta, PosDb } from "./db";
import { SellScreen } from "./sell-screen";
import { CASHIER_ID, seedDevice } from "./test-fixtures";
import { describeSync } from "./ui/sync-status";
import { PosContext } from "./use-pos-data";

describe("describeSync", () => {
  const base = { ...defaultSyncMeta(), lastSyncAt: Date.UTC(2026, 10, 7, 3, 42) };
  it("menulis status offline dengan antrean dan jam sinkron terakhir", () => {
    const v = describeSync({ sync: { ...base, online: false }, waiting: 3, now: Date.now(), timeZone: "Asia/Jakarta" });
    expect(v).toMatchObject({ tone: "warning", icon: "offline", text: "Offline · 3 transaksi menunggu · sinkron terakhir 10.42 WIB" });
  });
  it("membedakan online tersinkron, sedang sinkron, dicabut, dan offline terlalu lama", () => {
    expect(describeSync({ sync: { ...base, online: true }, waiting: 0, now: 0, timeZone: "Asia/Jakarta" }).text).toBe(
      "Online · semua tersinkron · sinkron terakhir 10.42 WIB",
    );
    expect(describeSync({ sync: { ...base, online: true }, waiting: 2, now: 0 }).icon).toBe("syncing");
    expect(describeSync({ sync: { ...base, authRevoked: true }, waiting: 2, now: 0 })).toMatchObject({
      tone: "danger",
      text: "Perangkat dicabut · 2 transaksi menunggu",
    });
    const long = describeSync({
      sync: { ...base, online: false },
      waiting: 1,
      now: base.lastSyncAt + 25 * 3600_000,
      offlineLimitSeconds: 86_400,
    });
    expect(long.offlineTooLong).toBe(true);
  });
  it("tidak bilang tersinkron kalau server masih menahan toko, dan memakai heartbeat sebagai kontak terakhir", () => {
    const hb = Date.UTC(2026, 10, 7, 3, 50);
    const fresh = { ...defaultSyncMeta(), online: true, lastHeartbeatAt: hb };
    expect(describeSync({ sync: fresh, waiting: 0, now: hb, timeZone: "Asia/Jakarta" }).text).toBe(
      "Online · semua tersinkron · sinkron terakhir 10.50 WIB",
    );
    const held = { ...fresh, onlineBlockedReason: "missing_seqs" };
    expect(describeSync({ sync: held, waiting: 0, now: hb, timeZone: "Asia/Jakarta" })).toMatchObject({
      tone: "warning",
      text: "Online · server menunggu transaksi yang hilang · sinkron terakhir 10.50 WIB",
    });
    expect(describeSync({ sync: { ...fresh, onlineBlockedReason: "pending_outbox" }, waiting: 0, now: hb }).text).toContain(
      "menunggu konfirmasi server",
    );
  });
});

describe("layar jual", () => {
  let db: PosDb;
  beforeEach(async () => {
    db = new PosDb(`ui-${Math.random()}`);
    await seedDevice(db);
    await db.meta.put({ key: "session", cashierId: CASHIER_ID, cashierName: "Rina", since: Date.now() });
    await db.meta.put({ key: "prefs", sound: false });
  });
  afterEach(() => db.close());

  it("scan → keranjang → F2 bayar tunai → struk, transaksi masuk outbox", async () => {
    render(
      <MemoryRouter>
        <PosContext.Provider value={{ db, engine: null }}>
          <SellScreen />
        </PosContext.Provider>
      </MemoryRouter>,
    );
    const scan = await screen.findByLabelText("Scan atau cari produk");
    await waitFor(() => expect(scan).toHaveFocus());

    fireEvent.change(scan, { target: { value: "8991234567890" } });
    fireEvent.keyDown(scan, { key: "Enter" });
    // Batas waktu longgar: suite lain (argon2) berjalan paralel dan memperlambat IndexedDB tiruan.
    expect(await screen.findByText("Ditambahkan: Serum Niacinamide 30 ml", undefined, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.getByText("1 produk · 1 unit")).toBeInTheDocument();

    // Kode yang tidak ada di katalog: pesan jelas, keranjang tidak berubah.
    fireEvent.change(scan, { target: { value: "0000000000000" } });
    fireEvent.keyDown(scan, { key: "Enter" });
    expect(await screen.findByText("Kode 0000000000000 tidak ada di katalog toko.")).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "F2" });
    const dialog = await screen.findByRole("dialog", { name: "Pembayaran" });
    const amount = within(dialog).getByLabelText("Nominal (Tunai)");
    // Burst scanner ke kolom nominal (jeda < 30 ms) diabaikan, bukan jadi pembayaran.
    // (Event.timeStamp diisi platform; event sinkron berturut-turut berjarak < 30 ms.)
    let typed = "";
    for (const ch of "8991234567890") {
      fireEvent.keyDown(amount, { key: ch });
      typed += ch;
      fireEvent.change(amount, { target: { value: typed } });
    }
    fireEvent.keyDown(amount, { key: "Enter" });
    expect(await within(dialog).findByText(/Isi scan masuk ke kolom nominal/)).toBeInTheDocument();
    expect(amount).toHaveValue("");

    fireEvent.change(amount, { target: { value: "200000" } });
    fireEvent.keyDown(amount, { key: "Enter" });
    expect(await within(dialog).findByText("Rp50.000")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Selesaikan transaksi" }));

    const receipt = await screen.findByRole("dialog", { name: "Transaksi tersimpan" });
    expect(within(receipt).getByText("Rp50.000")).toBeInTheDocument();
    expect(within(receipt).getByText("Tersimpan di perangkat, menunggu sinkron")).toBeInTheDocument();
    const rows = await db.outbox.toArray();
    expect(rows).toHaveLength(1);
    expect(rows[0]!.payload).toMatchObject({ total: 150_000, payments: [{ method: "CASH", amount: 200_000 }] });
    expect((await db.cart.get("current"))?.lines).toEqual([]);
  });

  it("serial QR yang tidak dikenal perangkat ditahan sampai produk dipilih (daftar serial lokal kosong)", async () => {
    await db.serials.clear();
    render(
      <MemoryRouter>
        <PosContext.Provider value={{ db, engine: null }}>
          <SellScreen />
        </PosContext.Provider>
      </MemoryRouter>,
    );
    const scan = await screen.findByLabelText("Scan atau cari produk");
    fireEvent.change(scan, { target: { value: `${window.location.origin}/v/7K9M2P4Q8R1S0T3V` } });
    fireEvent.keyDown(scan, { key: "Enter" });
    expect(
      (await screen.findAllByText(/Serial 7K9M2P4Q8R1S0T3V belum terdaftar di toko ini/)).length,
    ).toBeGreaterThan(0);
    fireEvent.change(scan, { target: { value: "TNR-02" } });
    fireEvent.keyDown(scan, { key: "Enter" });
    expect(await screen.findByText("Ditambahkan: Toner Centella 100 ml (serial 7K9M2P4Q8R1S0T3V)")).toBeInTheDocument();
  });
});
