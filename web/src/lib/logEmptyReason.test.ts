import { describe, expect, it } from 'vitest'

import { logEmptyReason } from './logEmptyReason'

describe('logEmptyReason', () => {
  // True only here: the stream is open and the container has not spoken.
  it('says the container is quiet only while the stream is live', () => {
    expect(logEmptyReason('streaming', '', 0)).toBe('No output yet.')
  })

  // The bug: every non-connecting state got "No output yet", which is a claim
  // about the pod. A stream that failed read nothing; it does not follow that
  // nothing was written.
  it('does not blame the container for a stream that failed', () => {
    const got = logEmptyReason('error', '', 0)
    expect(got).not.toBe('No output yet.')
    expect(got).toMatch(/failed/)
    // And it heads off the reading the old text invited.
    expect(got).toMatch(/not a sign the container is quiet/)
  })

  // "Yet" promises more. An ended stream has none coming.
  it('does not promise more output from a stream that ended', () => {
    const got = logEmptyReason('ended', '', 0)
    expect(got).not.toMatch(/yet/)
    expect(got).toMatch(/ended/)
  })

  // The filter is a fact about the lines, so it is only the answer once lines
  // exist. A stream that failed before delivering any is not a filter that
  // matched none.
  it('blames the filter only when there were lines to filter', () => {
    expect(logEmptyReason('streaming', 'needle', 42)).toBe('No lines match the filter.')
    expect(logEmptyReason('error', 'needle', 0)).toMatch(/failed/)
    expect(logEmptyReason('ended', 'needle', 0)).toMatch(/ended/)
  })

  // Lines arrived and none was filtered out, so the pane is empty for no
  // reason worth a sentence — but "yet" would still be wrong.
  it('does not say "yet" once output has already arrived', () => {
    expect(logEmptyReason('ended', '', 7)).toBe('No output.')
  })
})
