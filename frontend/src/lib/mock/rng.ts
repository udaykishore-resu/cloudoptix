import { mulberry32 } from "@/lib/utils";

/** One shared seeded RNG stream for the whole mock world, so every fixture
 * generator produces the same numbers on every render (SSR + CSR) and across
 * reloads — the mock demo has to look the same every time it's shown. */
let seed = 20260831;
let rand = mulberry32(seed);

export function resetRng(newSeed = 20260831) {
  seed = newSeed;
  rand = mulberry32(seed);
}

export function rf(): number {
  return rand();
}
export function ri(min: number, max: number): number {
  return Math.floor(rand() * (max - min + 1)) + min;
}
export function rpick<T>(arr: readonly T[]): T {
  return arr[Math.floor(rand() * arr.length)];
}
export function rpickN<T>(arr: readonly T[], n: number): T[] {
  const pool = [...arr];
  const out: T[] = [];
  for (let i = 0; i < n && pool.length; i++) {
    const idx = Math.floor(rand() * pool.length);
    out.push(pool.splice(idx, 1)[0]);
  }
  return out;
}
export function rbool(pTrue = 0.5): boolean {
  return rand() < pTrue;
}
/** Skewed-toward-low random float, useful for utilisation and error rates. */
export function rskew(min: number, max: number, power = 2): number {
  return min + Math.pow(rand(), power) * (max - min);
}
export function rid(prefix: string): string {
  const chars = "0123456789abcdefghjkmnpqrstvwxyz";
  let s = "";
  for (let i = 0; i < 20; i++) s += chars[Math.floor(rand() * chars.length)];
  return `${prefix}_${s}`;
}
