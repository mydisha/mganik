import "fake-indexeddb/auto";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { PosDb } from "./db";
import { LoginScreen } from "./login-screen";
import { makeBootstrap, seedDevice } from "./test-fixtures";
import { PosContext } from "./use-pos-data";

// Regresi: daftar kasir dulu memakai orderBy("name") pada store tanpa indeks
// name, sehingga layar login crash di browser sungguhan.
describe("layar login kasir", () => {
  let db: PosDb;
  beforeEach(async () => {
    db = new PosDb(`login-${Math.random()}`);
    const b = makeBootstrap();
    b.cashiers = [
      { ...b.cashiers[0], id: "00000000-0000-0000-0000-0000000000b2", name: "Budi" },
      { ...b.cashiers[0], name: "Ani" },
    ];
    await seedDevice(db, b);
  });
  afterEach(() => db.close());

  it("menampilkan kasir terurut nama tanpa crash", async () => {
    render(
      <MemoryRouter>
        <PosContext.Provider value={{ db, engine: null }}>
          <LoginScreen />
        </PosContext.Provider>
      </MemoryRouter>,
    );
    const ani = await screen.findByRole("button", { name: /Ani/ });
    const budi = await screen.findByRole("button", { name: /Budi/ });
    expect(ani.compareDocumentPosition(budi) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
