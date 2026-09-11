const ws = new WebSocket('ws://localhost:9224/devtools/page/18C8DCB0884C5DE863CED52DD56F198D')
let id = 0; const pending = new Map()
const send = (method, params = {}) => new Promise((resolve) => {
  const mid = ++id; pending.set(mid, resolve); ws.send(JSON.stringify({ id: mid, method, params }))
})
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id) } }
await new Promise((r) => (ws.onopen = r))
await send('Network.enable')
await send('Page.enable')
const reqs = []
ws.onmessage = (ev) => {
  const m = JSON.parse(ev.data)
  if (m.method === 'Network.requestWillBeSent' && m.params.request.url) {
    const u = m.params.request.url
    if (!/\.(js|css|png|jpg|svg|ico|woff)/.test(u)) reqs.push(u)
  }
}
await send('Page.navigate', { url: 'https://www.xn--ykq321c.com/?count=1' })
await new Promise((r) => setTimeout(r, 12000))
console.log('non-static requests:')
for (const u of reqs) console.log(' ', u.slice(0, 130))
ws.close()
