// A ULID (26 Crockford base32 characters: 48 bits of time, 80 of randomness),
// the id the daemon requires of an idempotent request. The clock and the
// random source are injected so a test needs neither.

const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/** @returns {Uint8Array} n random bytes from the browser's CSPRNG. */
export function cryptoRandom(n) {
  const bytes = new Uint8Array(n);
  globalThis.crypto.getRandomValues(bytes);
  return bytes;
}

/**
 * @param {number} nowMs the clock, in milliseconds since the epoch
 * @param {(n: number) => Uint8Array} random n random bytes
 * @returns {string} the ULID
 */
export function ulid(nowMs = Date.now(), random = cryptoRandom) {
  let t = Math.floor(nowMs);
  let time = "";
  for (let i = 0; i < 10; i++) {
    time = ALPHABET[t % 32] + time;
    t = Math.floor(t / 32);
  }
  const bytes = random(16);
  let rand = "";
  for (let i = 0; i < 16; i++) {
    // 32 divides 256, so the low five bits of a byte are uniform.
    rand += ALPHABET[bytes[i] & 31];
  }
  return time + rand;
}

/** The daemon's pattern for an id (its OpenAPI ULID schema). */
export const ULID_PATTERN = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
