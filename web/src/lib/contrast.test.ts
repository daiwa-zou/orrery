import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { contrast, luminance, over, parseHex, type RGB } from './contrast'

describe('contrast helpers', () => {
  it('matches the reference extremes', () => {
    const white = parseHex('#ffffff')
    const black = parseHex('#000000')
    expect(contrast(white, black)).toBeCloseTo(21, 1)
    expect(contrast(white, white)).toBeCloseTo(1, 5)
    expect(luminance(white)).toBeCloseTo(1, 5)
    expect(luminance(black)).toBeCloseTo(0, 5)
  })

  it('is symmetric', () => {
    const a = parseHex('#5980a6')
    const b = parseHex('#1a2028')
    expect(contrast(a, b)).toBeCloseTo(contrast(b, a), 10)
  })

  it('composites an overlay the way a translucent fill does', () => {
    const black = parseHex('#000000')
    const white = parseHex('#ffffff')
    expect(over(white, black, 0)).toEqual(black)
    expect(over(white, black, 1)).toEqual(white)
    expect(over(white, black, 0.5)).toEqual([128, 128, 128])
  })

  it('refuses a colour it cannot parse rather than returning zero', () => {
    expect(() => parseHex('nonsense')).toThrow()
    expect(() => parseHex('#12345')).toThrow()
    expect(parseHex('#abc')).toEqual(parseHex('#aabbcc'))
  })
})

/**
 * The theme's own tokens, read out of the stylesheet that defines them.
 *
 * Both blocks declare the same variable names, so the first occurrence is the
 * dark theme and the second is the light one.
 */
function themeTokens(): { dark: Record<string, string>; light: Record<string, string> } {
  const css = readFileSync(new URL('../index.css', import.meta.url), 'utf8')
  const dark: Record<string, string> = {}
  const light: Record<string, string> = {}
  for (const m of css.matchAll(/--color-([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,6})\s*;/g)) {
    const [, name, value] = m
    if (dark[name] === undefined) dark[name] = value
    else if (light[name] === undefined) light[name] = value
  }
  return { dark, light }
}

describe('focus indicator', () => {
  const { dark, light } = themeTokens()

  it('found both themes in index.css', () => {
    // If this fails the tokens moved and the assertions below are measuring
    // nothing, which is worse than measuring the wrong thing.
    for (const t of [dark, light]) {
      expect(t['accent']).toMatch(/^#/)
      expect(t['surface']).toMatch(/^#/)
      expect(t['ink']).toMatch(/^#/)
    }
    expect(dark['surface']).not.toBe(light['surface'])
  })

  // WCAG 2.2 §2.4.11 asks for at least 3:1 between the focused and unfocused
  // states of a control. Table rows are how every resource in this console is
  // opened, so their indicator has to clear it.
  it.each([['dark'], ['light']])('accent outline is visible against the %s surface', (name) => {
    const t = name === 'dark' ? dark : light
    const ratio = contrast(parseHex(t['accent']), parseHex(t['surface']))
    expect(ratio).toBeGreaterThanOrEqual(3)
  })

  // The treatment this replaced, kept as the reason it was replaced: a tint
  // of the ink colour cannot reach 3:1 at any opacity that still reads as a
  // tint, so "make it a bit stronger" was never the fix.
  it.each([['dark'], ['light']])('a subtle ink tint cannot reach 3:1 on the %s surface', (name) => {
    const t = name === 'dark' ? dark : light
    const surface: RGB = parseHex(t['surface'])
    const ink: RGB = parseHex(name === 'dark' ? t['ink'] : t['canvas'] ?? t['ink'])
    for (const alpha of [0.04, 0.08, 0.16]) {
      expect(contrast(over(ink, surface, alpha), surface)).toBeLessThan(3)
    }
  })
})

/**
 * The row is the app's primary navigation surface: reached with Tab, activated
 * with Enter. It must not opt out of the global :focus-visible outline, which
 * is what `focus:outline-none` did while offering a 1.10:1 tint instead.
 */
describe('DataTable rows keep their focus ring', () => {
  const source = readFileSync(new URL('../components/DataTable.tsx', import.meta.url), 'utf8')

  it('does not suppress the outline on focusable rows', () => {
    expect(source).not.toContain('focus:outline-none')
  })

  it('keeps the outline inside the row', () => {
    // The global rule offsets the outline outward, which on a table row draws
    // it over the neighbouring rows.
    expect(source).toContain('focus-visible:-outline-offset-2')
  })

  it('still marks rows focusable and activatable', () => {
    expect(source).toContain('tabIndex={onRowClick ? 0 : undefined}')
    expect(source).toContain("e.key === 'Enter'")
  })
})
