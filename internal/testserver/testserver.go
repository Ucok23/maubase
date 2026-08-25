// Package testserver boots a fully wired maubase server — real SQLite file,
// real migrations, real auth, oauth, owner, and REST services — behind a
// live HTTP listener, so tests in test/ exercise exactly the surface a
// real client sees. Nothing here reaches into internals; it exists purely
// to remove the boilerplate of standing the server up.
package testserver

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"maubase/internal/audit"
	"maubase/internal/auth"
	"maubase/internal/db"
	"maubase/internal/oauth"
	"maubase/internal/ownerauth"
	"maubase/internal/realtime"
	"maubase/internal/restapi"
	"maubase/internal/server"
	"maubase/internal/storage"
)

// Options configures what a test server starts with, beyond maubase's own
// built-in schema.
type Options struct {
	// BootstrapOwnerEmail/Password, if both set, seed a first owner-plane
	// account the way MAUBASE_BOOTSTRAP_OWNER_EMAIL/_PASSWORD would on a real
	// deployment's first run.
	BootstrapOwnerEmail    string
	BootstrapOwnerPassword string

	// Schema is extra DDL (CREATE TABLE ...) run after maubase's own
	// migrations and before REST collection discovery — this is how a
	// test gets its own application table(s) to exercise internal/restapi
	// against, standing in for what a deployment's migrations/ directory
	// would normally provide.
	Schema []string

	// LoginRateLimit/Window configure the login-endpoint throttle (see
	// internal/ratelimit and spec/maintenance.md). Zero value
	// (LoginRateLimit 0) disables it, which is what every Options{} not
	// specifically testing rate-limiting gets — otherwise every test that
	// logs in more than a handful of times would need to think about it.
	LoginRateLimit  int
	LoginRateWindow time.Duration

	// MaxUploadBytes caps a single file upload's size (see
	// internal/storage). Zero means "use the same 25MB default as
	// config.Load".
	MaxUploadBytes int64
}

// New starts a server on an ephemeral local port and returns its base URL
// (e.g. "http://127.0.0.1:53214"), with no owner account and no
// application schema beyond maubase's own tables.
func New(t *testing.T) string {
	t.Helper()
	return NewCustom(t, Options{})
}

// NewWithOwner is New, plus a first owner-plane account (role "owner")
// already bootstrapped and ready to log in with the given credentials.
func NewWithOwner(t *testing.T, email, password string) string {
	t.Helper()
	return NewCustom(t, Options{BootstrapOwnerEmail: email, BootstrapOwnerPassword: password})
}

// NewWithSchema is New, plus the given CREATE TABLE statements applied
// before REST collection discovery, so /api/data/{table} is live for them.
func NewWithSchema(t *testing.T, schema ...string) string {
	t.Helper()
	return NewCustom(t, Options{Schema: schema})
}

// NewWithLoginRateLimit is New, plus the login-endpoint throttle enabled
// at the given limit/window (see Options.LoginRateLimit).
func NewWithLoginRateLimit(t *testing.T, limit int, window time.Duration) string {
	t.Helper()
	return NewCustom(t, Options{LoginRateLimit: limit, LoginRateWindow: window})
}

// NewCustom is the general form; New/NewWithOwner/NewWithSchema are thin
// wrappers over it for the common cases.
func NewCustom(t *testing.T, opts Options) string {
	t.Helper()
	url, err := newCustom(t, opts)
	if err != nil {
		t.Fatalf("discover rest collections: %v", err)
	}
	return url
}

// NewCustomExpectingDiscoverError is NewCustom, but for the one scenario
// where opts is expected to fail schema discovery at startup — an
// invalid _policies row (spec/access-rules.md ACCESS-08) — rather than
// start successfully. It returns that error instead of failing the test,
// so the test can assert on it; every other setup failure (a bad Schema
// statement, say) still fails the test immediately via t.Fatalf, since
// those aren't what's under test here.
func NewCustomExpectingDiscoverError(t *testing.T, opts Options) error {
	t.Helper()
	_, err := newCustom(t, opts)
	if err == nil {
		t.Fatalf("want schema discovery to fail, but the server started successfully")
	}
	return err
}

func newCustom(t *testing.T, opts Options) (string, error) {
	t.Helper()

	// The issuer has to be known before the oauth server is built (it's
	// baked into JWT "iss" claims and the token endpoint URL), so the
	// listener is opened first purely to learn which port we got.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	issuer := "http://" + lis.Addr().String()

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	if err := db.Migrate(sqlDB); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, stmt := range opts.Schema {
		if _, err := sqlDB.Exec(stmt); err != nil {
			t.Fatalf("apply test schema %q: %v", stmt, err)
		}
	}

	authSvc := auth.NewService(sqlDB)

	oauthSvc, err := oauth.NewServer(context.Background(), sqlDB, authSvc, issuer)
	if err != nil {
		t.Fatalf("init oauth server: %v", err)
	}

	ownerSvc := ownerauth.NewService(sqlDB)
	if opts.BootstrapOwnerEmail != "" {
		if _, err := ownerSvc.Bootstrap(context.Background(), opts.BootstrapOwnerEmail, opts.BootstrapOwnerPassword); err != nil {
			t.Fatalf("bootstrap owner: %v", err)
		}
	}

	broker := realtime.NewBroker()

	registry, err := restapi.Discover(context.Background(), sqlDB)
	if err != nil {
		return "", err
	}
	restapiSvc := restapi.NewServer(sqlDB, registry, oauthSvc, broker)

	storageBackend, err := storage.NewLocalBackend(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatalf("init storage backend: %v", err)
	}
	maxUploadBytes := opts.MaxUploadBytes
	if maxUploadBytes == 0 {
		maxUploadBytes = 25 << 20 // 25MB, matching config's default
	}
	storageSvc := storage.NewServer(sqlDB, storageBackend, oauthSvc, maxUploadBytes)

	realtimeSvc := realtime.NewServer(broker, oauthSvc)

	auditLog := audit.New(sqlDB)

	httpSrv := &http.Server{Handler: server.New(authSvc, oauthSvc, ownerSvc, restapiSvc, storageSvc, realtimeSvc, auditLog, opts.LoginRateLimit, opts.LoginRateWindow)}
	go httpSrv.Serve(lis) //nolint:errcheck // Serve always returns non-nil; Close() below triggers it deliberately
	t.Cleanup(func() { httpSrv.Close() })

	return issuer, nil
}
