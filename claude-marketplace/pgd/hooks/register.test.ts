// Tests of the mod's registration: the hooks it registers at registration time.
import { expect, test } from 'claude-code/testing'
import { register } from './register'

test('registering the mod registers the band and alert ui.render hooks and session.start', () => {
  const seen: Array<{ name: string; component?: string }> = []
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ;(register as any)((name: string, sel: unknown) => {
    seen.push({ name, component: (sel as { component?: string } | undefined)?.component })
  })
  const renders = seen.filter(s => s.name === 'ui.render').map(s => s.component)
  expect(renders).toContain('AbovePrompt')
  expect(renders).toContain('PromptHint')
  expect(seen.map(s => s.name)).toContain('session.start')
})
