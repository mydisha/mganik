import { describe, expect, it } from "vitest";
import { normalizePhone, normalizePin } from "./input";

describe("normalizePhone", () => {
  it.each([
    ["0812 3456 7890", "+6281234567890"],
    ["+62 812-3456-789", "+628123456789"],
    ["62812.3456.7890", "+6281234567890"],
    ["(0812) 345 678", "+62812345678"],
    ["812345678901", "+62812345678901"],
  ])("%s → %s", (raw, expected) => {
    expect(normalizePhone(raw)).toBe(expected);
  });

  it.each([
    "",
    "0712 3456 7890", // bukan seluler
    "+1 812 3456 7890", // kode negara lain
    "0812", // terlalu pendek
    "0812 3456 7890 1234", // terlalu panjang
    "０８１２３４５６７８９０", // digit unicode lebar penuh
    "٠٨١٢٣٤٥٦٧٨٩٠", // digit Arab
    "0812+3456789",
  ])("menolak %s", (raw) => {
    expect(normalizePhone(raw)).toBeNull();
  });
});

describe("normalizePin", () => {
  it("membuang spasi dan tanda hubung lalu menjadi huruf besar", () => {
    expect(normalizePin(" abcd-efgh jk ")).toBe("ABCDEFGHJK");
  });

  it("menghitung per karakter unicode, bukan per byte", () => {
    expect(normalizePin("äöü1234567")).toBe("ÄÖÜ1234567");
    expect(normalizePin("😀😀😀😀😀1234")).toBeNull();
    expect(normalizePin("ABC")).toBeNull();
  });
});
