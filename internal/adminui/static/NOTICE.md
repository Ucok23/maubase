This directory has one vendored third-party asset, embedded via
`go:embed` (see `../server.go`) so the admin UI needs no CDN and no JS
build step:

- `htmx.min.js` — [htmx](https://htmx.org) v1.9.12, BSD 2-Clause license.
  https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js
  Not modified from upstream. Update by re-downloading a pinned version
  from the URL above.

`admin.css` is hand-written for this project (see the comment at its
top) — no framework, nothing vendored.
