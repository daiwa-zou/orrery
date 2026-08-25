import { describe, expect, it } from 'vitest'
import { decodeSecretValue } from './secrets'

const b64 = (s: string) => Buffer.from(s, 'utf-8').toString('base64')

describe('decodeSecretValue', () => {
  it('decodes printable text', () => {
    expect(decodeSecretValue(b64('hunter2'))).toEqual({
      text: 'hunter2',
      binary: false,
      size: 7,
    })
  })

  it('keeps multi-line values like certificates in PEM form', () => {
    const pem = '-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n'
    expect(decodeSecretValue(b64(pem)).text).toBe(pem)
  })

  it('flags binary payloads instead of rendering mojibake', () => {
    // A DER certificate starts with 0x30 0x82 — invalid as UTF-8 text.
    const der = Buffer.from([0x30, 0x82, 0x01, 0x00, 0xff, 0xfe]).toString('base64')
    expect(decodeSecretValue(der)).toEqual({ binary: true, size: 6 })
  })

  it('flags valid UTF-8 that is full of control bytes', () => {
    expect(decodeSecretValue(b64('a\x00b'))).toEqual({ binary: true, size: 3 })
  })

  it('shows undecodable input as-is rather than nothing', () => {
    const out = decodeSecretValue('not/base64!!')
    expect(out.binary).toBe(false)
    expect(out.text).toBe('not/base64!!')
  })
})
