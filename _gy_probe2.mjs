const ws = new WebSocket('ws://localhost:9224/devtools/page/18C8DCB0884C5DE863CED52DD56F198D')
let id = 0; const pending = new Map()
const send = (method, params = {}) => new Promise((resolve) => {
  const mid = ++id; pending.set(mid, resolve); ws.send(JSON.stringify({ id: mid, method, params }))
})
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id) } }
await new Promise((r) => (ws.onopen = r))
await send('Runtime.enable')
const r = await send('Runtime.evaluate', { expression: 'document.body.innerText', returnByValue: true })
console.log(r.result.result.value)
ws.close()
