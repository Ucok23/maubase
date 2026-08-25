This directory has vendored third-party assets, embedded via `go:embed`
(see `../server.go`) so the admin UI needs no CDN and no JS build step:

- `htmx.min.js` — [htmx](https://htmx.org) v1.9.12, BSD 2-Clause license.
  https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js
  Not modified from upstream. Update by re-downloading a pinned version
  from the URL above.

- `codemirror.min.js`, `codemirror.min.css`, `sql.min.js` —
  [CodeMirror](https://codemirror.net) 5.65.16 (the classic drop-in-
  script-tags version — CodeMirror 6 is ESM-only and needs a bundler,
  which this project deliberately doesn't have), MIT license. Backs SQL
  Studio's syntax-highlighted editor (`templates/sql_studio.html`).
  Minified copies pulled from jsDelivr's on-the-fly minifier, pinned to
  5.65.16:
  https://cdn.jsdelivr.net/npm/codemirror@5.65.16/lib/codemirror.min.js
  https://cdn.jsdelivr.net/npm/codemirror@5.65.16/lib/codemirror.min.css
  https://cdn.jsdelivr.net/npm/codemirror@5.65.16/mode/sql/sql.min.js
  Not modified beyond that minification. No theme file is vendored —
  the `.cm-s-maubase` theme in `admin.css` is hand-written to match this
  project's own color tokens rather than importing a mismatched one.

`admin.css` is hand-written for this project (see the comment at its
top) — no framework, nothing vendored.
