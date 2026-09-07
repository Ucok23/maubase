// Package sdkjs embeds the built @maubase/client output (dist/) so
// cmd/maubase can vendor it into a scaffolded project via
// `maubase init --js-client` (see spec/project-init.md INIT-10, issue
// #162) — with no npm-publish step and no second version number to keep
// in sync with the Go module's own (maubase version, #159): whatever
// dist/ contains here IS what a given maubase binary ships, because it's
// compiled straight into the binary at `go build`/`go install` time.
//
// dist/ is committed here specifically so this embed — and therefore
// `go install .../maubase@vX.Y.Z` — has something to embed at all (see
// sdk/js/.gitignore, which no longer excludes it). Run `npm run build`
// in sdk/js/ after changing anything under src/, and commit the
// resulting dist/ changes in the same PR as the source change that
// produced them — same discipline as any other generated-and-committed
// artifact in this repo (see internal/adminui's vendored static/).
package sdkjs

import "embed"

//go:embed dist
var DistFS embed.FS
