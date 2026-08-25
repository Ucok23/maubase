package oauth

import "github.com/ory/fosite"

// dbClient is the row shape for oauth_clients, decoupled from fosite's
// Client interface so storage.go doesn't need to know about fosite.Client
// construction details in more than one place.
type dbClient struct {
	ID            string
	Secret        []byte
	Public        bool
	RedirectURIs  []string
	GrantTypes    []string
	ResponseTypes []string
	Scopes        []string
}

func (c dbClient) toFosite() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID:            c.ID,
		Secret:        c.Secret,
		RedirectURIs:  c.RedirectURIs,
		GrantTypes:    c.GrantTypes,
		ResponseTypes: c.ResponseTypes,
		Scopes:        c.Scopes,
		Public:        c.Public,
	}
}

// AllowedScopes is the fixed scope vocabulary this server understands.
// Dynamic client registration (register.go) rejects any scope outside this
// list; the consent screen (handlers.go) only ever offers these. Extend it
// as real resources (auto-REST tables, etc.) get scopes of their own.
var AllowedScopes = []string{"profile", "records:read", "records:write", "files:read", "files:write", "offline_access"}

func isAllowedScope(scope string) bool {
	for _, s := range AllowedScopes {
		if s == scope {
			return true
		}
	}
	return false
}
