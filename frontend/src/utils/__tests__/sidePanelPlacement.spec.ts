import { describe, expect, it } from 'vitest'
import { placeSidePanel } from '../sidePanelPlacement'

const viewport = { width: 390, height: 844 }
const anchor = (left: number, top: number) => ({ left, right: left + 16, top, height: 16 })

describe('placeSidePanel', () => {
  it('keeps the preferred right-hand placement when it fits', () => {
    expect(placeSidePanel(anchor(1000, 400), { width: 300, height: 200 }, { width: 1820, height: 790 }))
      .toEqual({ x: 1024, y: 408, attached: true })
  })

  it('flips to the left side when the right side would overflow', () => {
    const placed = placeSidePanel(anchor(1700, 400), { width: 300, height: 200 }, { width: 1820, height: 790 })
    expect(placed).toEqual({ x: 1392, y: 408, attached: false })
  })

  it('clamps inside narrow viewports when neither side fits', () => {
    const placed = placeSidePanel(anchor(340, 400), { width: 360, height: 200 }, viewport)
    expect(placed.x).toBe(22)
    expect(placed.x + 360).toBeLessThanOrEqual(viewport.width - 8)
    expect(placed.attached).toBe(false)
  })

  it('clamps vertically near the top and bottom edges', () => {
    expect(placeSidePanel(anchor(10, 5), { width: 100, height: 300 }, viewport).y).toBe(158)
    expect(placeSidePanel(anchor(10, 830), { width: 100, height: 300 }, viewport).y).toBe(686)
  })
})
