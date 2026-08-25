package oauth

import "html/template"

// Minimal, dependency-free HTML for the two screens a human ever sees in
// this flow. Real styling / branding belongs to the future admin UI; this
// is deliberately plain so it never needs its own asset pipeline.
var loginTpl = template.Must(template.New("login").Parse(`<!doctype html>
<html><head><title>Sign in</title></head>
<body style="font-family:system-ui;max-width:360px;margin:4rem auto">
<h2>Sign in</h2>
<p>{{.ClientName}} wants to connect to your account.</p>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
<form method="POST" action="/oauth/authorize">
<input type="hidden" name="step" value="login">
<input type="hidden" name="oauth_request" value="{{.OAuthRequest}}">
<div><label>Email<br><input type="email" name="email" required autofocus></label></div>
<div style="margin-top:.5rem"><label>Password<br><input type="password" name="password" required></label></div>
<button type="submit" style="margin-top:1rem">Sign in</button>
</form>
</body></html>`))

var consentTpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html><head><title>Authorize</title></head>
<body style="font-family:system-ui;max-width:360px;margin:4rem auto">
<h2>Authorize {{.ClientName}}</h2>
<p>Signed in as {{.Email}}. This app is requesting:</p>
<form method="POST" action="/oauth/authorize">
<input type="hidden" name="step" value="consent">
<input type="hidden" name="oauth_request" value="{{.OAuthRequest}}">
{{range .Scopes}}
<div><label><input type="checkbox" name="granted" value="{{.}}" checked> {{.}}</label></div>
{{end}}
<button type="submit" name="decision" value="allow" style="margin-top:1rem">Allow</button>
<button type="submit" name="decision" value="deny">Deny</button>
</form>
</body></html>`))
