// Bunyi umpan balik scan (Web Audio, tanpa file suara). Berhasil: satu nada
// tinggi pendek. Gagal: dua nada rendah. Dimatikan lewat preferensi "Bunyi
// scan" di header; kalau browser memblokir audio, diam tanpa error.

let ctx: AudioContext | null = null;

function context(): AudioContext | null {
  if (typeof window === "undefined" || !("AudioContext" in window)) return null;
  ctx ??= new AudioContext();
  if (ctx.state === "suspended") void ctx.resume().catch(() => undefined);
  return ctx;
}

function tone(ac: AudioContext, freq: number, start: number, duration: number) {
  const osc = ac.createOscillator();
  const gain = ac.createGain();
  osc.type = "square";
  osc.frequency.value = freq;
  gain.gain.setValueAtTime(0.0001, start);
  gain.gain.exponentialRampToValueAtTime(0.08, start + 0.01);
  gain.gain.exponentialRampToValueAtTime(0.0001, start + duration);
  osc.connect(gain).connect(ac.destination);
  osc.start(start);
  osc.stop(start + duration + 0.02);
}

export function playScanSound(kind: "ok" | "error", enabled: boolean): void {
  if (!enabled) return;
  try {
    const ac = context();
    if (!ac) return;
    const t = ac.currentTime;
    if (kind === "ok") {
      tone(ac, 1320, t, 0.08);
    } else {
      tone(ac, 220, t, 0.12);
      tone(ac, 180, t + 0.15, 0.16);
    }
  } catch {
    // Audio bukan fungsi inti; abaikan.
  }
}
