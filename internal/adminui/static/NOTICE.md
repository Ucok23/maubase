These two files are vendored third-party assets, embedded via `go:embed`
(see `../server.go`) so the admin UI needs no CDN and no JS build step:

- `htmx.min.js` — [htmx](https://htmx.org) v1.9.12, BSD 2-Clause license.
  https://unpkg.com/htmx.org@1.9.12/dist/htmx.min.js
- `pico.min.css` — [Pico CSS](https://picocss.com) v2.1.1, MIT license.
  https://cdn.jsdelivr.net/npm/@picocss/pico@2/css/pico.min.css

Neither is modified from upstream. Update by re-downloading a pinned
version from the URLs above.
