// Package social lets an end user sign in via a third-party identity
// provider ("Continue with Google"/"Continue with GitHub") instead of
// email+password. Deliberately separate from internal/oauth, which is
// maubase acting as an OAuth *authorization server* for third-party
// apps — this is the opposite direction: maubase acting as an OAuth
// *client* to Google/GitHub. See spec/social-login.md.
package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// Identity is what a provider told us about the person who just
// authorized us, boiled down to the two things internal/auth actually
// needs: a stable id to key an oauth_identities row on, and an email to
// match against (or create) a local account with.
type Identity struct {
	ProviderUserID string
	Email          string
}

// Provider wraps an oauth2.Config with the provider-specific logic for
// turning a token into an Identity — the one part that can't be generic,
// since Google and GitHub each return profile data in their own shape
// (and GitHub, additionally, often omits email from its main profile
// response entirely, requiring a second call).
type Provider struct {
	Name   string
	config oauth2.Config
	fetch  func(ctx context.Context, client *http.Client) (Identity, error)
}

// AuthCodeURL builds the URL to send the user's browser to, carrying
// state (an opaque, server-generated CSRF token the caller must
// generate, store — e.g. in a short-lived cookie — and verify against
// the callback's own state parameter).
func (p Provider) AuthCodeURL(state string) string {
	return p.config.AuthCodeURL(state)
}

// Exchange redeems an authorization code for a token, per the standard
// OAuth2 authorization code flow.
func (p Provider) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	return p.config.Exchange(ctx, code)
}

// FetchIdentity uses tok to call the provider's own profile endpoint(s)
// and returns what it found.
func (p Provider) FetchIdentity(ctx context.Context, tok *oauth2.Token) (Identity, error) {
	client := p.config.Client(ctx, tok)
	return p.fetch(ctx, client)
}

// NewGoogle and NewGitHub (below) take every endpoint URL explicitly
// rather than hardcoding them, so a test can point a Provider at a local
// fake server instead of the real thing — see
// test/social_login_test.go. Google()/GitHub() are what production code
// actually calls, filling in the real URLs.

func NewGoogle(clientID, clientSecret, redirectURL, authURL, tokenURL, userInfoURL string) Provider {
	return Provider{
		Name: "google",
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
			Scopes:   []string{"openid", "email"},
		},
		fetch: func(ctx context.Context, client *http.Client) (Identity, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, userInfoURL, nil)
			if err != nil {
				return Identity{}, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return Identity{}, fmt.Errorf("fetch google userinfo: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
				return Identity{}, fmt.Errorf("google userinfo returned %d: %s", resp.StatusCode, body)
			}
			var body struct {
				Sub   string `json:"sub"`
				Email string `json:"email"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				return Identity{}, fmt.Errorf("decode google userinfo: %w", err)
			}
			if body.Sub == "" {
				return Identity{}, fmt.Errorf("google userinfo response had no sub")
			}
			return Identity{ProviderUserID: body.Sub, Email: body.Email}, nil
		},
	}
}

func Google(clientID, clientSecret, redirectURL string) Provider {
	return NewGoogle(clientID, clientSecret, redirectURL,
		endpoints.Google.AuthURL, endpoints.Google.TokenURL,
		"https://www.googleapis.com/oauth2/v3/userinfo")
}

func NewGitHub(clientID, clientSecret, redirectURL, authURL, tokenURL, userInfoURL, emailsURL string) Provider {
	return Provider{
		Name: "github",
		config: oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL},
			Scopes:   []string{"read:user", "user:email"},
		},
		fetch: func(ctx context.Context, client *http.Client) (Identity, error) {
			var profile struct {
				ID    int64  `json:"id"`
				Email string `json:"email"`
			}
			if err := getJSON(ctx, client, userInfoURL, &profile); err != nil {
				return Identity{}, fmt.Errorf("fetch github user: %w", err)
			}
			if profile.ID == 0 {
				return Identity{}, fmt.Errorf("github user response had no id")
			}

			email := profile.Email
			if email == "" {
				// GitHub omits email from /user entirely unless the
				// account has made one public; the dedicated emails
				// endpoint is the only reliable way to get the
				// account's actual (verified, primary) address.
				var emails []struct {
					Email    string `json:"email"`
					Primary  bool   `json:"primary"`
					Verified bool   `json:"verified"`
				}
				if err := getJSON(ctx, client, emailsURL, &emails); err == nil {
					for _, e := range emails {
						if e.Primary && e.Verified {
							email = e.Email
							break
						}
					}
				}
			}
			return Identity{ProviderUserID: fmt.Sprintf("%d", profile.ID), Email: email}, nil
		},
	}
}

func GitHub(clientID, clientSecret, redirectURL string) Provider {
	return NewGitHub(clientID, clientSecret, redirectURL,
		endpoints.GitHub.AuthURL, endpoints.GitHub.TokenURL,
		"https://api.github.com/user", "https://api.github.com/user/emails")
}

func getJSON(ctx context.Context, client *http.Client, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
