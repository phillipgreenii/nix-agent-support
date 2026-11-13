// Stub Messages API server for the pgd opt-in checks: every POST /v1/messages answers one fixed reply
// (streamed or not); the reply text is read from the file named by argv[2] (default: the word ok).
// Prints 'PORT <n>' once listening on 127.0.0.1. No outside host is reached.
import http from 'node:http'
import fs from 'node:fs'
const text = process.argv[2] ? fs.readFileSync(process.argv[2], 'utf8') : 'ok'
const srv = http.createServer((req, res) => {
  let body = ''
  req.on('data', c => (body += c))
  req.on('end', () => {
    if (req.url.startsWith('/v1/messages/count_tokens')) {
      res.setHeader('content-type', 'application/json')
      return res.end('{"input_tokens":10}')
    }
    if (!req.url.startsWith('/v1/messages')) {
      res.statusCode = 404
      return res.end('{}')
    }
    let stream = false
    try { stream = JSON.parse(body).stream } catch {}
    const msg = { id: 'msg_stub', type: 'message', role: 'assistant', model: 'claude-stub', content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 10, output_tokens: 1 } }
    if (!stream) {
      res.setHeader('content-type', 'application/json')
      return res.end(JSON.stringify({ ...msg, content: [{ type: 'text', text }], stop_reason: 'end_turn' }))
    }
    res.writeHead(200, { 'content-type': 'text/event-stream' })
    const ev = (n, d) => res.write(`event: ${n}\ndata: ${JSON.stringify({ type: n, ...d })}\n\n`)
    ev('message_start', { message: msg })
    ev('content_block_start', { index: 0, content_block: { type: 'text', text: '' } })
    ev('content_block_delta', { index: 0, delta: { type: 'text_delta', text } })
    ev('content_block_stop', { index: 0 })
    ev('message_delta', { delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 20 } })
    ev('message_stop', {})
    res.end()
  })
})
srv.listen(0, '127.0.0.1', () => console.log('PORT ' + srv.address().port))
