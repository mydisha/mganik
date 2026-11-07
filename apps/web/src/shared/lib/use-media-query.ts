import { useSyncExternalStore } from "react";

const MOBILE_QUERY = "(max-width: 767.98px)";

function subscribe(onChange: () => void) {
  const media = globalThis.matchMedia?.(MOBILE_QUERY);
  media?.addEventListener("change", onChange);
  return () => media?.removeEventListener("change", onChange);
}

function snapshot() {
  return globalThis.matchMedia?.(MOBILE_QUERY).matches ?? false;
}

/** true di bawah 768 px, tempat tabel berubah menjadi kartu. */
export function useIsMobile(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => false);
}
