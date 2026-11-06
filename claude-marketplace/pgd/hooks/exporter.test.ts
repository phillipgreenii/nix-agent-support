import { expect, test } from 'claude-code/testing'
import { exportMetrics, initExporter } from './exporter'
import { record, resetMetrics } from './metrics'
import { setOptions } from './options'
import { initLog } from './log'
import { DEFAULT_OPTIONS, parseOptions } from './types'
import { settle, stubHost } from './testkit'

test('without an endpoint exportMetrics returns at once and touches nothing', async () => {
  const stub = stubHost()
  initExporter(stub.host)
  setOptions({ ...DEFAULT_OPTIONS })
  record('pgd_invalidate_total', 1)
  await exportMetrics()
  expect(stub.calls).toEqual([])
})

test('with an endpoint it posts the snapshot once, never concurrently', async () => {
  resetMetrics()
  const stub = stubHost()
  const posts: Array<{ url: string; body: string }> = []
  let release: () => void = () => {}
  stub.host.http.fetch = (url: string, init: { body: string }) =>
    new Promise<void>(res => {
      posts.push({ url, body: init.body })
      release = res
    })
  initExporter(stub.host)
  setOptions(parseOptions(JSON.stringify({ metrics: { endpoint: 'http://127.0.0.1:4318/v1/metrics' } })))
  record('pgd_invalidate_total', 1)
  const first = exportMetrics()
  void exportMetrics() // in flight: ignored
  await settle()
  expect(posts.length).toBe(1)
  expect(posts[0].url).toBe('http://127.0.0.1:4318/v1/metrics')
  expect(JSON.parse(posts[0].body).metrics[0].name).toBe('pgd_invalidate_total')
  release()
  await first
})

test('a failing endpoint never throws', async () => {
  const stub = stubHost()
  stub.host.http.fetch = async () => {
    throw new Error('refused')
  }
  initLog(stub.host)
  initExporter(stub.host)
  setOptions(parseOptions(JSON.stringify({ metrics: { endpoint: 'http://x' } })))
  await exportMetrics()
})

test('an empty or non-string endpoint option parses as unset', () => {
  expect(parseOptions(JSON.stringify({ metrics: { endpoint: '' } })).metrics).toEqual({})
  expect(parseOptions(JSON.stringify({ metrics: { endpoint: 3 } })).metrics).toEqual({})
})
