// Package e2e_test drives a real, running baas server purely over HTTP —
// no reaching into internal packages beyond testserver.New — so these
// tests see exactly what an external client sees. Each test references
// the spec scenario ID (see /spec) it verifies.
package e2e_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// newClient returns an http.Client with its own cookie jar, so a session
// established via one request (e.g. signup) is carried by later requests
// on the same client, like a real browser session. Redirects are not
// followed automatically: several flows here need to inspect a 303's
// Location header directly, including ones pointing at a redirect_uri
// that doesn't really exist.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func decodeJSONMap(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	return out
}

// pkcePair returns a PKCE code_verifier and its S256 code_challenge, as a
// client starting an authorization code flow would generate.
func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// signUp creates a fresh account. Per IDNT-01, this also signs the caller
// in: the client's cookie jar ends up holding a valid session.
func signUp(t *testing.T, client *http.Client, baseURL, email, password string) {
	t.Helper()
	resp := postJSON(t, client, baseURL+"/api/auth/signup", map[string]string{
		"email": email, "password": password,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup: want 201, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	resp.Body.Close()
}

// registerClient POSTs /oauth/register and returns the status code and
// decoded JSON body, letting callers assert on either a success or an
// error shape without the helper picking a side.
func registerClient(t *testing.T, baseURL string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(baseURL+"/oauth/register", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /oauth/register: %v", err)
	}
	return resp.StatusCode, decodeJSONMap(t, resp)
}

// registerPublicClient registers a "none"-auth (PKCE-only) client and
// fails the test immediately if that doesn't succeed, for tests where
// registration itself isn't what's under test.
func registerPublicClient(t *testing.T, baseURL, redirectURI, scope string) (clientID string) {
	t.Helper()
	status, out := registerClient(t, baseURL, map[string]any{
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"scope":                      scope,
	})
	if status != http.StatusCreated {
		t.Fatalf("register client: want 201, got %d: %v", status, out)
	}
	id, _ := out["client_id"].(string)
	if id == "" {
		t.Fatalf("register client: no client_id in response: %v", out)
	}
	return id
}

// authorizeParams builds the query for GET /oauth/authorize.
func authorizeParams(clientID, redirectURI, scope, state, challenge string) url.Values {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", scope)
	v.Set("state", state)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	return v
}

// locationQuery parses the query parameters off a response's Location
// header, failing the test if there isn't one.
func locationQuery(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatalf("expected a Location header, got none (status %d): %s", resp.StatusCode, bodyString(t, resp))
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	return u.Query()
}

// approveConsent drives an already-authenticated client through the
// consent screen and returns the final redirect response (a 303 back to
// the client's redirect_uri, per AUTHZ-03/04).
func approveConsent(t *testing.T, client *http.Client, baseURL string, authorizeQuery url.Values, scopes []string, allow bool) *http.Response {
	t.Helper()

	getResp, err := client.Get(baseURL + "/oauth/authorize?" + authorizeQuery.Encode())
	if err != nil {
		t.Fatalf("GET /oauth/authorize: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /oauth/authorize: want 200 (consent screen), got %d", getResp.StatusCode)
	}

	form := url.Values{}
	form.Set("step", "consent")
	form.Set("oauth_request", authorizeQuery.Encode())
	for _, sc := range scopes {
		form.Add("granted", sc)
	}
	if allow {
		form.Set("decision", "allow")
	} else {
		form.Set("decision", "deny")
	}

	resp, err := client.PostForm(baseURL+"/oauth/authorize", form)
	if err != nil {
		t.Fatalf("POST /oauth/authorize (consent): %v", err)
	}
	return resp
}

// authorizeAndGetToken drives a full first-time authorize+consent+token-
// exchange for a signed-in client, granting exactly the given scopes, and
// returns the decoded token response. It's the "happy path" setup shared
// by tests whose actual assertion is further downstream (token grants,
// resource access).
func authorizeAndGetToken(t *testing.T, client *http.Client, baseURL, clientID, redirectURI string, scopes []string) map[string]any {
	t.Helper()
	verifier, challenge := pkcePair(t)
	q := authorizeParams(clientID, redirectURI, strings.Join(scopes, " "), "somestate12345678", challenge)
	resp := approveConsent(t, client, baseURL, q, scopes, true)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize: want 303, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}
	code := locationQuery(t, resp).Get("code")
	if code == "" {
		t.Fatalf("authorize: expected a code in the redirect")
	}
	status, tok := exchangeCode(t, baseURL, code, redirectURI, clientID, verifier)
	if status != http.StatusOK {
		t.Fatalf("token exchange: want 200, got %d: %v", status, tok)
	}
	return tok
}

// exchangeCode redeems an authorization code at the token endpoint.
func exchangeCode(t *testing.T, baseURL, code, redirectURI, clientID, verifier string) (int, map[string]any) {
	t.Helper()
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(baseURL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("POST /oauth/token: %v", err)
	}
	return resp.StatusCode, decodeJSONMap(t, resp)
}
