// 共享 canvas 工具：DPR 缩放、坐标映射、颜色常量。
// K 线与量化净值曲线共用，避免两处各写一份 DPR 逻辑。

/** 与 style.css 的语义色保持一致（canvas 读不到 CSS 变量）。 */
export const CHART_COLORS = {
  bg: '#0d1117',
  grid: '#1d2530',
  gridStrong: '#2c3746',
  text: '#748092',
  text2: '#b3bdcc',
  up: '#ff5d5d',
  down: '#2fbf87',
  cross: '#8b95a6',
  accent: '#e8b34b',
  ma5: '#f0c877',
  ma10: '#4c9dff',
  ma20: '#c07bff',
  ma60: '#9aa6b8',
} as const

export const CHART_MONO = "ui-monospace, 'SF Mono', Menlo, monospace"

/**
 * 按 CSS 尺寸和 devicePixelRatio 调整 backing store，返回已设置好缩放的 2D 上下文。
 * 尺寸过小时返回 null，调用方直接跳过绘制。
 */
export function prepareCanvas(
  canvas: HTMLCanvasElement,
  width: number,
  height: number,
): CanvasRenderingContext2D | null {
  if (width < 40 || height < 60) return null
  const dpr = window.devicePixelRatio || 1
  const pxW = Math.round(width * dpr)
  const pxH = Math.round(height * dpr)
  if (canvas.width !== pxW || canvas.height !== pxH) {
    canvas.width = pxW
    canvas.height = pxH
  }
  canvas.style.width = width + 'px'
  canvas.style.height = height + 'px'
  const ctx = canvas.getContext('2d')
  if (!ctx) return null
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, height)
  ctx.fillStyle = CHART_COLORS.bg
  ctx.fillRect(0, 0, width, height)
  return ctx
}

/** 在矩形内画虚线十字与标签底（价格轴/日期轴复用）。 */
export function labelBox(
  ctx: CanvasRenderingContext2D,
  text: string,
  x: number,
  y: number,
  align: 'left' | 'center',
  width: number,
  height: number,
): void {
  ctx.fillStyle = CHART_COLORS.gridStrong
  if (align === 'left') {
    ctx.fillRect(x, y - height / 2, width, height)
    ctx.fillStyle = CHART_COLORS.text2
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    ctx.fillText(text, x + 4, y)
  } else {
    ctx.fillRect(x - width / 2, y, width, height)
    ctx.fillStyle = CHART_COLORS.text2
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    ctx.fillText(text, x, y + 2)
  }
}
