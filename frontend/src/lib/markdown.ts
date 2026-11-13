// 极简 Markdown → 安全 HTML。支持：标题、列表、引用、代码块、GFM 表格、行内 code/粗体、链接。
// 表格只识别「表头行 + |---| 分隔行」的标准写法；其它以 | 开头的段落仍降级为等宽文本，
// 避免把普通文本误判成表格。先转义再生成标签，因此不存在 XSS。

const FENCE = String.fromCharCode(96).repeat(3)

function esc(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

function inline(s: string): string {
  let out = esc(s)
  // 行内代码先于其它标记处理：代码里的 * / _ 不再当成强调。
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>')
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/__([^_]+)__/g, '<strong>$1</strong>')
  out = out.replace(/(?<![\w*])\*([^*\n]+)\*(?![\w*])/g, '<em>$1</em>')
  out = out.replace(/~~([^~]+)~~/g, '<del>$1</del>')
  out = out.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer noopener">$1</a>')
  // 裸链接：只在前面不是引号/等号时转（避免把已经生成的 <a href> 再包一层）。
  out = out.replace(/(^|[\s(（])(https?:\/\/[^\s<)）]+)/g, '$1<a href="$2" target="_blank" rel="noreferrer noopener">$2</a>')
  return out
}

/** 一行是否是列表项；返回缩进层级、是否有序与实际内容。 */
function listItemOf(line: string): { indent: number; ordered: boolean; text: string } | null {
  const m = /^(\s*)([-*+]|\d+\.)\s+(.*)$/.exec(line)
  if (!m) return null
  const spaces = (m[1] ?? '').replace(/\t/g, '  ').length
  return { indent: Math.floor(spaces / 2), ordered: /\d+\./.test(m[2] ?? ''), text: m[3] ?? '' }
}

/** 把一行 GFM 表格拆成单元格文本：去掉首尾竖线后按 | 切分，并去掉每格首尾空白。 */
function splitRow(line: string): string[] {
  let s = line.trim()
  if (s.startsWith('|')) s = s.slice(1)
  if (s.endsWith('|')) s = s.slice(0, -1)
  return s.split('|').map((cell) => cell.trim())
}

/** 判断一行是否是 GFM 表格的分隔行（如 |---|:--:|---:|）；要求含竖线，避免把水平线误判成表格。 */
function isTableSeparator(line: string): boolean {
  const s = line.trim()
  if (!s.includes('|')) return false
  const cells = splitRow(s)
  if (cells.length === 0) return false
  return cells.every((cell) => /^:?-{3,}:?$/.test(cell))
}

/** 从分隔行解析每列对齐方式：center（两端冒号）/ right（右冒号）/ left（左冒号）/ ''（未指定）。 */
function columnAligns(separator: string): Array<'left' | 'center' | 'right' | ''> {
  return splitRow(separator).map((cell) => {
    const left = cell.startsWith(':')
    const right = cell.endsWith(':')
    if (left && right) return 'center'
    if (right) return 'right'
    if (left) return 'left'
    return ''
  })
}

/** 判断单元格是否像数字（含千分位、百分号、正负号、货币符号），未显式指定对齐时用它做右对齐。 */
function looksNumeric(cell: string): boolean {
  return /^[+-]?(?:[¥￥$]\s*)?\d[\d,]*(?:\.\d+)?%?$/.test(cell)
}

/** 生成单元格 class：显式对齐优先，其次数字右对齐，其余保持左对齐。 */
function cellClass(base: string, align: 'left' | 'center' | 'right' | '', numeric: boolean): string {
  if (align === 'center') return base + ' md-ta-center'
  if (align === 'right') return base + ' md-ta-right'
  if (align === 'left') return base + ' md-ta-left'
  return numeric ? base + ' md-num' : base
}

/** 渲染一张 GFM 表格；列数以表头为准，缺失单元格补空、多余单元格截断。 */
function renderTable(header: string, separator: string, rows: string[]): string {
  const aligns = columnAligns(separator)
  const heads = splitRow(header)
  const cols = heads.length
  const out: string[] = ['<div class="md-table-wrap"><table class="md-table"><thead><tr>']
  heads.forEach((cell, i) => {
    out.push('<th class="' + cellClass('md-th', aligns[i] ?? '', looksNumeric(cell)) + '">' + inline(cell) + '</th>')
  })
  out.push('</tr></thead><tbody>')
  for (const row of rows) {
    const cells = splitRow(row)
    out.push('<tr>')
    for (let i = 0; i < cols; i++) {
      const cell = cells[i] ?? ''
      out.push('<td class="' + cellClass('md-td', aligns[i] ?? '', looksNumeric(cell)) + '">' + inline(cell) + '</td>')
    }
    out.push('</tr>')
  }
  out.push('</tbody></table></div>')
  return out.join('')
}

export function mdToHtml(src: string): string {
  if (!src) return ''
  const lines = src.replace(/\r\n/g, '\n').split('\n')
  const out: string[] = []
  // 列表栈：每层记 <ul>/<ol> 标签，缩进变化时开/关层（LLM 输出常见两层嵌套）。
  const listStack: Array<'ul' | 'ol'> = []
  const closeList = (to = 0): void => {
    while (listStack.length > to) {
      out.push('</' + (listStack.pop() ?? 'ul') + '>')
    }
  }
  let i = 0
  while (i < lines.length) {
    const line = lines[i] ?? ''
    const trimmed = line.trim()

    if (trimmed.startsWith(FENCE)) {
      closeList()
      const buf: string[] = []
      i += 1
      while (i < lines.length && !(lines[i] ?? '').trim().startsWith(FENCE)) {
        buf.push(lines[i] ?? '')
        i += 1
      }
      i += 1
      out.push('<pre class="md-pre">' + esc(buf.join('\n')) + '</pre>')
      continue
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line)
    if (heading) {
      closeList()
      const level = Math.min((heading[1] ?? '#').length + 2, 5)
      out.push('<h' + level + ' class="md-h">' + inline(heading[2] ?? '') + '</h' + level + '>')
      i += 1
      continue
    }

    const item = listItemOf(line)
    if (item) {
      // 层级：比当前栈浅就关层，比它深就开一层（有序/无序按标记类型决定）。
      const depth = Math.min(item.indent, 2) + 1
      while (listStack.length > depth) {
        out.push('</' + (listStack.pop() ?? 'ul') + '>')
      }
      if (listStack.length === depth) {
        const tag = listStack[depth - 1]
        if ((tag === 'ol') !== item.ordered) {
          out.push('</' + (tag ?? 'ul') + '>')
          const next = item.ordered ? 'ol' : 'ul'
          out.push('<' + next + ' class="md-list' + (depth > 1 ? ' md-list-nested' : '') + '">')
          listStack[depth - 1] = next
        }
      } else {
        while (listStack.length < depth) {
          const next = item.ordered ? 'ol' : 'ul'
          out.push('<' + next + ' class="md-list' + (listStack.length > 0 ? ' md-list-nested' : '') + '">')
          listStack.push(next)
        }
      }
      out.push('<li>' + inline(item.text) + '</li>')
      i += 1
      continue
    }

    if (trimmed.startsWith('|')) {
      closeList()
      const next = (lines[i + 1] ?? '').trim()
      if (next !== '' && isTableSeparator(next)) {
        const header = lines[i] ?? ''
        i += 2
        const rows: string[] = []
        while (i < lines.length && (lines[i] ?? '').trim().startsWith('|')) {
          rows.push((lines[i] ?? '').trim())
          i += 1
        }
        out.push(renderTable(header, next, rows))
        continue
      }
      const buf: string[] = []
      while (i < lines.length && (lines[i] ?? '').trim().startsWith('|')) {
        buf.push((lines[i] ?? '').trim())
        i += 1
      }
      out.push('<pre class="md-pre md-table">' + esc(buf.join('\n')) + '</pre>')
      continue
    }

    if (trimmed.startsWith('>')) {
      closeList()
      const buf: string[] = []
      while (i < lines.length && (lines[i] ?? '').trim().startsWith('>')) {
        buf.push((lines[i] ?? '').replace(/^\s*>\s?/, ''))
        i += 1
      }
      out.push('<blockquote class="md-quote">' + inline(buf.join(' ')) + '</blockquote>')
      continue
    }

    if (trimmed === '' || trimmed === '---') {
      closeList()
      i += 1
      continue
    }

    closeList()
    out.push('<p class="md-p">' + inline(trimmed) + '</p>')
    i += 1
  }
  closeList()
  return out.join('')
}
