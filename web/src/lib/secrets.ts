/** Decoding helpers for the Secret and ConfigMap data viewers. */

export interface DecodedValue {
  /** The decoded text, when it is printable. */
  text?: string
  /** True when the payload does not decode to renderable text. */
  binary: boolean
  /** Decoded size in bytes. */
  size: number
}

/**
 * Decodes one base64 Secret value. Certificates, keystores and other binary
 * payloads are flagged rather than dumped as mojibake.
 */
export function decodeSecretValue(b64: string): DecodedValue {
  let raw: string
  try {
    raw = atob(b64)
  } catch {
    // Not base64 at all — surface what is there rather than nothing.
    return { text: b64, binary: false, size: b64.length }
  }
  const bytes = Uint8Array.from(raw, (ch) => ch.charCodeAt(0))
  let text: string
  try {
    text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
  } catch {
    return { binary: true, size: bytes.length }
  }
  // Valid UTF-8 can still be a binary blob; control characters (other than
  // whitespace) are the tell.
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x08\x0b\x0c\x0e-\x1f]/.test(text)) {
    return { binary: true, size: bytes.length }
  }
  return { text, binary: false, size: bytes.length }
}
