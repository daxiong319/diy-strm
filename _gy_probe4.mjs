const ws = new WebSocket('ws://localhost:9224/devtools/page/18C8DCB0884C5DE863CED52DD56F198D')
let id = 0; const pending = new Map()
const send = (method, params = {}) => new Promise((resolve) => {
  const mid = ++id; pending.set(mid, resolve); ws.send(JSON.stringify({ id: mid, method, params }))
})
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id) } }
await new Promise((r) => (ws.onopen = r))
await send('Runtime.enable')
const r1 = await send('Runtime.evaluate', { expression: `(() => {
  const html = document.documentElement.outerHTML
  return JSON.stringify({ len: html.length, hasJS: [...document.scripts].map(s=>s.src).filter(Boolean).slice(0,5) })
})()`, returnByValue: true })
console.log(r1.result.result.value)
// 找域名列表在哪个 JS 或全局变量里
const r2 = await send('Runtime.evaluate', { expression: `(() => {
  const out = []
  for (const k of Object.keys(window)) {
    try {
      const v = JSON.stringify(window[k])
      if (v && (v.includes('教父') || v.includes('hgeme'))) out.push(k + '=' + v.slice(0, 300))
    } catch (e) {}
  }
  return out.join('\n---\n').slice(0, 1500)
})()`, returnByValue: true })
console.log(r2.result.result.value)
ws.close()
