# maubase.dev (working title)

The project's landing page — plain static HTML/CSS, no build step, no
dependencies. Lives in this subdirectory deliberately so a Go
contributor working on the backend never has to touch it, and
`go build ./...`/`go test ./...` never see it.

## Preview locally

```sh
cd site && python3 -m http.server 8000
# then open http://localhost:8000/
```

## Scope (for now)

Landing page only — hero, feature overview, a "why self-host" pitch,
and a quickstart. No docs section yet (see the repo's own README.md and
`spec/*.md` for actual documentation in the meantime). Not yet deployed
anywhere — GitHub Pages + a custom domain is the plan, but that's a
separate, deliberately deferred step; this directory should keep
working as a plain static site regardless of how it ends up hosted.

## Design

Instrument-datasheet aesthetic (flat bordered panels, mono spec labels)
rather than the usual SaaS-dashboard gradient look — see the comment at
the top of `styles.css`. Type: Archivo (display), Source Serif 4
(body), IBM Plex Mono (code/labels), all via Google Fonts.
