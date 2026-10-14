// Initial JS/CSS (entry chunk + its static imports), gzipped, against the
// budgets in docs/SPEC.md section 7. Run after `npm run build`.
import { readFileSync } from 'node:fs'
import { gzipSync } from 'node:zlib'

const BUDGET = { js: 180 * 1024, css: 30 * 1024 }
const manifest = JSON.parse(readFileSync('dist/.vite/manifest.json', 'utf8'))
const entry = Object.values(manifest).find((c) => c.isEntry)
const seen = new Set()
const files = { js: new Set(), css: new Set() }

;(function walk(chunk) {
  if (seen.has(chunk.file)) return
  seen.add(chunk.file)
  files.js.add(chunk.file)
  for (const css of chunk.css ?? []) files.css.add(css)
  for (const key of chunk.imports ?? []) walk(manifest[key])
})(entry)

const gz = (f) => gzipSync(readFileSync(`dist/${f}`), { level: 9 }).length
let failed = false
for (const kind of ['js', 'css']) {
  const total = [...files[kind]].reduce((sum, f) => sum + gz(f), 0)
  const ok = total <= BUDGET[kind]
  failed ||= !ok
  console.log(`${ok ? 'ok  ' : 'FAIL'} initial ${kind}: ${(total / 1024).toFixed(1)} KB gzipped (budget ${BUDGET[kind] / 1024} KB)`)
  for (const f of files[kind]) console.log(`       ${f} ${(gz(f) / 1024).toFixed(1)} KB`)
}
process.exit(failed ? 1 : 0)
