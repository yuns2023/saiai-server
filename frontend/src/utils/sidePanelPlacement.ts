export interface SidePanelPlacement {
  /** Left edge of the panel (viewport px). */
  x: number
  /** Vertical centre of the panel (viewport px); the panel uses -translate-y-1/2. */
  y: number
  /** False when the panel had to move away from the anchor's right side (hide the arrow). */
  attached: boolean
}

/**
 * Place a fixed panel to the right of an anchor, vertically centred, then keep it
 * inside the viewport: flip to the left side, else clamp horizontally; clamp vertically.
 */
export function placeSidePanel(
  anchor: Pick<DOMRect, 'top' | 'left' | 'right' | 'height'>,
  panel: { width: number; height: number },
  viewport: { width: number; height: number } = { width: window.innerWidth, height: window.innerHeight },
  gap = 8,
  margin = 8,
): SidePanelPlacement {
  const preferredX = anchor.right + gap
  let x = preferredX
  if (x + panel.width > viewport.width - margin) {
    const leftSide = anchor.left - gap - panel.width
    x = leftSide >= margin ? leftSide : Math.max(margin, viewport.width - margin - panel.width)
  }
  const preferredY = anchor.top + anchor.height / 2
  const half = panel.height / 2
  const minY = margin + half
  const maxY = viewport.height - margin - half
  const y = maxY < minY ? viewport.height / 2 : Math.min(Math.max(preferredY, minY), maxY)
  return { x, y, attached: x === preferredX && y === preferredY }
}
