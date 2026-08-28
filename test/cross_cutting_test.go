package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"maubase/internal/email"
	"maubase/internal/realtime"
	"maubase/internal/social"
	"maubase/internal/testserver"
)

// Scenarios: spec/cross-cutting.md

// TestCrossCutting_SeveralOptionalFeaturesCombinedOnOneDeployment stands
// up a single server with more optional testserver.Options features
// active at once than any other test in this suite does — a Redis-backed
// realtime relay, a fake social login provider, a _policies override, a
// bootstrapped owner account, and an email sender, all simultaneously —
// and drives each one end to end, checking every feature still behaves
// exactly as its own spec describes with nothing else active.
func TestCrossCutting_SeveralOptionalFeaturesCombinedOnOneDeployment(t *testing.T) {
	// XFEAT-01
	mr := miniredis.RunT(t)
	relay, err := realtime.NewRedisRelay("redis://"+mr.Addr(), "test:xfeat")
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}

	googleProvider := fakeGoogleProvider(t, "xfeat-google-uid", "xfeat-social@example.com")
	sender := email.NewFakeSender()

	baseURL := testserver.NewCustom(t, testserver.Options{
		BootstrapOwnerEmail: bootstrapEmail, BootstrapOwnerPassword: bootstrapPassword,
		Schema:              []string{notesSchema, policyRow("notes", "read", "shared")},
		Relay:               relay,
		SocialProviders:     map[string]social.Provider{"google": googleProvider},
		SocialLoginRedirect: "https://app.example.com/welcome",
		EmailSender:         sender,
	})

	// --- Customer A: social login -> OAuth token -> auto-REST -> realtime -> storage ---
	socialClient := newClient(t)
	state := startSocialLogin(t, socialClient, baseURL, "google")
	cbResp := socialCallback(t, socialClient, baseURL, "google", "fake-code", state)
	if cbResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("social callback: want 303, got %d: %s", cbResp.StatusCode, bodyString(t, cbResp))
	}
	if signedInEmail := meEmail(t, socialClient, baseURL); signedInEmail != "xfeat-social@example.com" {
		t.Fatalf("want the social account signed in, got email %q", signedInEmail)
	}

	clientID := registerPublicClient(t, baseURL, testRedirectURI, "records:read records:write files:read files:write")
	tok := authorizeAndGetToken(t, socialClient, baseURL, clientID, testRedirectURI,
		[]string{"records:read", "records:write", "files:read", "files:write"})
	token, _ := tok["access_token"].(string)
	if token == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}

	rc := connectRealtime(t, baseURL, token)
	subscribe(t, rc, "notes")

	createResp := doAuthed(t, http.MethodPost, baseURL+"/api/data/notes", token, map[string]any{"title": "combined-feature-note", "body": "x"})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create note: want 201, got %d: %s", createResp.StatusCode, bodyString(t, createResp))
	}
	created := decodeJSONMap(t, createResp)
	id, _ := created["id"].(string)

	ev := readEvent(t, rc)
	if ev.Type != "created" || ev.Collection != "notes" || ev.Record["title"] != "combined-feature-note" {
		t.Fatalf("want the realtime subscriber (via the Redis relay) to see the create, got %+v", ev)
	}

	// A separately-scoped witness token proves the _policies read:shared
	// override is honored alongside everything else active.
	witnessToken := restToken(t, baseURL, "xfeat-witness@example.com", []string{"records:read", "records:write"})
	witnessGet := doAuthed(t, http.MethodGet, baseURL+"/api/data/notes/"+id, witnessToken, nil)
	if witnessGet.StatusCode != http.StatusOK {
		t.Fatalf("witness get (read:shared): want 200, got %d", witnessGet.StatusCode)
	}

	uploadResp := uploadFile(t, baseURL, token, "hello.txt", []byte("hello from xfeat"))
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: want 201, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
	}
	fileID, _ := decodeJSONMap(t, uploadResp)["id"].(string)
	contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+fileID+"/content", token, nil)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("fetch upload content: want 200, got %d", contentResp.StatusCode)
	}
	if body := bodyString(t, contentResp); body != "hello from xfeat" {
		t.Fatalf("want the uploaded content back, got %q", body)
	}

	// --- Customer B: independent password-based account + forgot-password ---
	signUp(t, newClient(t), baseURL, "xfeat-pwreset@example.com", "originalpassword1")
	fpResp := forgotPassword(t, newClient(t), baseURL, "xfeat-pwreset@example.com")
	if fpResp.StatusCode != http.StatusNoContent {
		t.Fatalf("forgot-password: want 204, got %d: %s", fpResp.StatusCode, bodyString(t, fpResp))
	}
	sent := sender.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0].HTML, "token=") {
		t.Fatalf("want a reset email captured by the fake sender, got %+v", sent)
	}

	// --- Owner: the admin UI sees the same row, alongside everything else ---
	owner := newClient(t)
	adminUILogin(t, owner, baseURL, bootstrapEmail, bootstrapPassword)
	rowsBody := bodyString(t, doGetNoRedirect(t, owner, baseURL+"/admin/ui/data/notes"))
	if !strings.Contains(rowsBody, "combined-feature-note") {
		t.Fatalf("want the owner's admin UI to see the note created above, got: %s", rowsBody)
	}
}
