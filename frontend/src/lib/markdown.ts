// 极简 Markdown → 安全 HTML。支持：标题、列表、引用、代码块、行内 code/粗体、链接。
// 表格不解释，整块降级为等宽文本（研究记录里够用，且不会渲染错列）。
// 先转义再生成标签，因此不存在 XSS。

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
  out = out.replace(/`([^`]+)`/g, '<code>$1</code>')
  out = out.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g, '<a href="$2" target="_blank" rel="noreferrer noopener">$1</a>')
  return out
}

export function mdToHtml(src: string): string {
  if (!src) return ''
  const lines = src.replace(/\r\n/g, '\n').split('\n')
  const out: string[] = []
  let listOpen = false
  const closeList = (): void => {
    if (listOpen) {
      out.push('</ul>')
      listOpen = false
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

    if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
      if (!listOpen) {
        out.push('<ul class="md-ul">')
        listOpen = true
      }
      out.push('<li>' + inline(line.replace(/^\s*([-*+]|\d+\.)\s+/, '')) + '</li>')
      i += 1
      continue
    }

    if (trimmed.startsWith('|')) {
      closeList()
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
