/**
 * WCAG relative luminance and contrast, for asserting that the theme's own
 * tokens are usable rather than eyeballing them.
 *
 * The focus indicator is the reason this exists. WCAG 2.2 asks for at least
 * 3:1 between the focused and unfocused states of a control (2.4.11), and a
 * subtle background tint cannot get there at any opacity anyone would call
 * subtle: four percent of the ink colour over the surface is 1.10:1, and even
 * sixteen percent only reaches 1.57:1. An outline in the accent colour is
 * 3.95:1. That is a fact about the palette, so it belongs in a test that fails
 * when the palette changes rather than in a comment nobody re-checks.
 */

export type RGB = readonly [number, number, number]

/** Parses "#rrggbb" (or "#rgb"). Throws on anything else, since these are our
 *  own design tokens and a silent zero would make a test pass for the wrong
 *  reason. */
export function parseHex(hex: string): RGB {
  const h = hex.trim().replace(/^#/, '')
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h
  if (!/^[0-9a-fA-F]{6}$/.test(full)) {
    throw new Error(`not a hex colour: ${hex}`)
  }
  return [
    parseInt(full.slice(0, 2), 16),
    parseInt(full.slice(2, 4), 16),
    parseInt(full.slice(4, 6), 16),
  ] as const
}

/** Composites `fg` over `bg` at the given alpha, as a translucent overlay does. */
export function over(fg: RGB, bg: RGB, alpha: number): RGB {
  return [
    Math.round(fg[0] * alpha + bg[0] * (1 - alpha)),
    Math.round(fg[1] * alpha + bg[1] * (1 - alpha)),
    Math.round(fg[2] * alpha + bg[2] * (1 - alpha)),
  ] as const
}

/** WCAG relative luminance. */
export function luminance(c: RGB): number {
  const channel = (v: number) => {
    const s = v / 255
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(c[0]) + 0.7152 * channel(c[1]) + 0.0722 * channel(c[2])
}

/** WCAG contrast ratio, between 1 and 21. Order does not matter. */
export function contrast(a: RGB, b: RGB): number {
  const la = luminance(a)
  const lb = luminance(b)
  const hi = Math.max(la, lb)
  const lo = Math.min(la, lb)
  return (hi + 0.05) / (lo + 0.05)
}
