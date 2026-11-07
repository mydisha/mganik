import { useLiveQuery } from "dexie-react-hooks";
import { CircleAlert } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { EmptyState } from "@/shared/ui/empty-state";
import { updateSyncMeta } from "./db";
import { SCANNER_MAX_GAP_MS } from "./lib/scan";
import { isPinFormat, PIN_LENGTH, pinLockedUntil, verifyCashierPin } from "./pin";
import { posStrings as s } from "./strings";
import { NumPad } from "./ui/num-pad";
import { PosButton } from "./ui/pos-button";
import { useDevice, usePos } from "./use-pos-data";

// Login kasir: pilih nama, lalu PIN 6 digit lewat numpad atau keyboard.
// Verifikasi lokal (argon2id), jadi tetap bisa masuk saat offline.
export function LoginScreen() {
  const { db, engine } = usePos();
  const device = useDevice();
  const cashiers = useLiveQuery(() => db.cashiers.toArray().then((rows) => rows.sort((a, b) => a.name.localeCompare(b.name, "id"))), [db]);
  const [cashierId, setCashierId] = useState<string | null>(null);
  const [pin, setPinState] = useState("");
  const pinRef = useRef("");
  const setPin = useCallback((next: string | ((p: string) => string)) => {
    pinRef.current = typeof next === "function" ? next(pinRef.current) : next;
    setPinState(pinRef.current);
  }, []);
  const [checking, setChecking] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [lockedUntil, setLockedUntil] = useState<number | null>(null);
  const tz = device?.timezone;

  useEffect(() => {
    void pinLockedUntil(db).then(setLockedUntil);
  }, [db]);

  // Kunci habis waktunya: buka numpad lagi tanpa refresh.
  useEffect(() => {
    if (!lockedUntil) return;
    const t = setTimeout(() => {
      setLockedUntil(null);
      setMessage(null);
    }, Math.max(0, lockedUntil - Date.now()) + 50);
    return () => clearTimeout(t);
  }, [lockedUntil]);

  const cashier = cashiers?.find((c) => c.id === cashierId) ?? null;

  const submit = useCallback(
    async (value: string) => {
      if (!cashierId || !isPinFormat(value)) return;
      setChecking(true);
      const res = await verifyCashierPin(db, cashierId, value);
      setChecking(false);
      setPin("");
      if (res.ok) return; // Sesi baru → layar jual tampil lewat liveQuery.
      switch (res.reason) {
        case "wrong":
          setMessage(s.login.wrong(res.remaining));
          break;
        case "locked":
          setLockedUntil(res.until);
          setMessage(s.login.locked(res.until, tz));
          break;
        case "hash_invalid":
          setMessage(s.login.hashInvalid);
          break;
        case "engine_unavailable":
          setMessage(s.login.engineUnavailable);
          break;
        case "unknown_cashier":
          setMessage(s.login.unknownCashier);
          break;
        case "format":
          break;
      }
    },
    [cashierId, db, tz, setPin],
  );

  const press = useCallback(
    (d: string) => {
      if (checking || lockedUntil || pinRef.current.length >= PIN_LENGTH) return;
      const next = pinRef.current + d;
      setPin(next);
      if (next.length === PIN_LENGTH) void submit(next);
    },
    [checking, lockedUntil, submit, setPin],
  );

  useEffect(() => {
    if (!cashierId) return;
    let lastKeyAt = Number.NEGATIVE_INFINITY;
    let burstUntil = 0;
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.timeStamp || performance.now();
      const gap = t - lastKeyAt;
      lastKeyAt = t;
      // Burst scanner (jeda < 30 ms) di layar PIN: buang isian, abaikan sisa burst.
      if (e.key.length === 1 || e.key === "Enter") {
        if (gap < SCANNER_MAX_GAP_MS || t < burstUntil) {
          e.preventDefault();
          burstUntil = t + 300;
          setPin("");
          return;
        }
      }
      if (/^\d$/.test(e.key)) {
        e.preventDefault();
        press(e.key);
      } else if (e.key === "Backspace") {
        e.preventDefault();
        setPin((p) => p.slice(0, -1));
      } else if (e.key === "Escape") {
        setCashierId(null);
        setPin("");
        setMessage(null);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [cashierId, press, setPin]);

  const requestRefresh = async () => {
    await updateSyncMeta(db, { bootstrapRequestedAt: Date.now() });
    engine?.kick(true);
  };

  if (cashiers === undefined) {
    return (
      <p role="status" className="text-base text-muted-foreground">
        {s.boot.loading}
      </p>
    );
  }

  if (cashiers.length === 0) {
    return (
      <EmptyState
        title={s.login.noCashiersTitle}
        description={s.login.noCashiersBody}
        action={
          <>
            <PosButton posSize="pos" onClick={() => void requestRefresh()}>
              {s.device.refresh}
            </PosButton>
            <PosButton posSize="pos" variant="outline" asChild>
              <Link to="/pos/setup">{s.login.deviceSettings}</Link>
            </PosButton>
          </>
        }
      />
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-md flex-col gap-6">
      <h1 className="text-xl font-semibold">{cashier ? s.login.pinFor(cashier.name) : s.login.title}</h1>

      {!cashier ? (
        <section aria-labelledby="pos-cashier-list" className="flex flex-col gap-3">
          <h2 id="pos-cashier-list" className="text-base font-medium text-muted-foreground">
            {s.login.chooseCashier}
          </h2>
          <ul className="grid gap-3 sm:grid-cols-2">
            {cashiers.map((c) => (
              <li key={c.id}>
                <PosButton
                  posSize="pos"
                  variant="outline"
                  className="w-full justify-start truncate"
                  onClick={() => {
                    setCashierId(c.id);
                    setPin("");
                    if (!lockedUntil) setMessage(null);
                  }}
                >
                  {c.name}
                </PosButton>
              </li>
            ))}
          </ul>
        </section>
      ) : (
        <section className="flex flex-col items-center gap-5" aria-describedby="pos-pin-hint">
          <p id="pos-pin-hint" className="text-sm text-muted-foreground">
            {s.login.pinHint}
          </p>
          <div className="flex gap-3" role="img" aria-label={s.login.pinProgress(pin.length)}>
            {Array.from({ length: PIN_LENGTH }, (_, i) => (
              <span
                key={i}
                aria-hidden="true"
                className={`size-5 rounded-full border-2 border-foreground ${i < pin.length ? "bg-foreground" : "bg-transparent"}`}
              />
            ))}
          </div>
          <p role="status" aria-live="polite" className="min-h-5 text-sm text-muted-foreground">
            {checking ? s.login.checking : ""}
          </p>
          {message && (
            <p role="alert" className="flex w-full items-start gap-2 rounded-md border border-danger-border bg-danger p-3 text-sm text-danger-foreground">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              {message}
            </p>
          )}
          <NumPad
            className="w-full max-w-[13.5rem]"
            onDigit={press}
            onBackspace={() => setPin((p) => p.slice(0, -1))}
            onClear={() => setPin("")}
            disabled={checking || Boolean(lockedUntil)}
          />
          <PosButton
            posSize="touch"
            variant="ghost"
            onClick={() => {
              setCashierId(null);
              setPin("");
            }}
          >
            {s.login.otherCashier}
          </PosButton>
        </section>
      )}
    </div>
  );
}
