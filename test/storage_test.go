package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/Ucok23/maubase/internal/storage"
	"github.com/Ucok23/maubase/internal/testserver"
)

// Scenarios: spec/storage.md (STOR-01..08)

// storageToken registers a public client scoped for files:read/write,
// signs up a fresh user, and drives them through consent for the given
// scopes, returning a ready-to-use access token.
func storageToken(t *testing.T, baseURL, email string, scopes []string) string {
	t.Helper()
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "files:read files:write")
	client := newClient(t)
	signUp(t, client, baseURL, email, "correcthorse")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, scopes)
	at, _ := tok["access_token"].(string)
	if at == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}
	return at
}

// uploadFile POSTs a multipart/form-data upload to /api/storage/files.
// The part carries no explicit Content-Type, so the server falls back to
// application/octet-stream (see STOR-03's assertion on that).
func uploadFile(t *testing.T, baseURL, token, filename string, content []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/storage/files", &body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/storage/files: %v", err)
	}
	return resp
}

func TestStorage_UploadThenListAndGet(t *testing.T) {
	baseURL := testserver.New(t)
	token := storageToken(t, baseURL, "upload@example.com", []string{"files:read", "files:write"})

	// STOR-01
	uploadResp := uploadFile(t, baseURL, token, "hello.txt", []byte("hello world"))
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: want 201, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
	}
	created := decodeJSONMap(t, uploadResp)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("upload response missing id: %v", created)
	}
	if created["filename"] != "hello.txt" {
		t.Fatalf("want filename hello.txt, got %v", created)
	}

	// STOR-02
	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files", token, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: want 200, got %d: %s", listResp.StatusCode, bodyString(t, listResp))
	}
	listBody := decodeJSONMap(t, listResp)
	records, _ := listBody["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("want 1 file listed, got %v", listBody)
	}

	// STOR-03: metadata
	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id, token, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get metadata: want 200, got %d: %s", getResp.StatusCode, bodyString(t, getResp))
	}

	// STOR-03: content
	contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id+"/content", token, nil)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("get content: want 200, got %d", contentResp.StatusCode)
	}
	defer contentResp.Body.Close()
	got, err := io.ReadAll(contentResp.Body)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("want content %q, got %q", "hello world", got)
	}
	if ct := contentResp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("want Content-Type application/octet-stream, got %q", ct)
	}
}

func TestStorage_DeleteRemovesFile(t *testing.T) {
	baseURL := testserver.New(t)
	token := storageToken(t, baseURL, "delete@example.com", []string{"files:read", "files:write"})

	uploadResp := uploadFile(t, baseURL, token, "bye.txt", []byte("bye"))
	created := decodeJSONMap(t, uploadResp)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("upload: no id in %v", created)
	}

	// STOR-04
	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/storage/files/"+id, token, nil)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id, token, nil)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", getResp.StatusCode)
	}
	contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id+"/content", token, nil)
	if contentResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get content after delete: want 404, got %d", contentResp.StatusCode)
	}
}

func TestStorage_InvisibleToOtherCallers(t *testing.T) {
	baseURL := testserver.New(t)
	owner := storageToken(t, baseURL, "owner@example.com", []string{"files:read", "files:write"})
	other := storageToken(t, baseURL, "other@example.com", []string{"files:read", "files:write"})

	uploadResp := uploadFile(t, baseURL, owner, "secret.txt", []byte("secret"))
	created := decodeJSONMap(t, uploadResp)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("upload: no id in %v", created)
	}

	// STOR-05
	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id, other, nil)
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("other caller get metadata: want 404, got %d", getResp.StatusCode)
	}
	contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id+"/content", other, nil)
	if contentResp.StatusCode != http.StatusNotFound {
		t.Fatalf("other caller get content: want 404, got %d", contentResp.StatusCode)
	}
	delResp := doAuthed(t, http.MethodDelete, baseURL+"/api/storage/files/"+id, other, nil)
	if delResp.StatusCode != http.StatusNotFound {
		t.Fatalf("other caller delete: want 404, got %d", delResp.StatusCode)
	}

	// File must still be reachable by its real owner afterward.
	ownerGet := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id, owner, nil)
	if ownerGet.StatusCode != http.StatusOK {
		t.Fatalf("owner get after other's attempts: want 200, got %d", ownerGet.StatusCode)
	}
}

func TestStorage_RoutesRequireScope(t *testing.T) {
	baseURL := testserver.New(t)

	// STOR-06: no token at all.
	noTokenResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files", "", nil)
	if noTokenResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %d", noTokenResp.StatusCode)
	}

	// STOR-06: a token with the wrong scope (files:read only, used for a
	// write route).
	readOnly := storageToken(t, baseURL, "readonly@example.com", []string{"files:read"})
	uploadResp := uploadFile(t, baseURL, readOnly, "x.txt", []byte("x"))
	if uploadResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upload with files:read-only token: want 401, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
	}
}

func TestStorage_ExportIncludesFileMetadata(t *testing.T) {
	// STOR-07
	baseURL := testserver.New(t)
	client := newClient(t)
	signUp(t, client, baseURL, "export@example.com", "correcthorse")

	// Reuse the session cookie as a bearer-token-free upload path isn't
	// available; instead get an OAuth token for the same account via its
	// existing session, matching the identity_account_test.go pattern.
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "files:read files:write")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"files:read", "files:write"})
	at, _ := tok["access_token"].(string)
	if at == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}

	uploadResp := uploadFile(t, baseURL, at, "export.txt", []byte("data"))
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: want 201, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
	}
	uploaded := decodeJSONMap(t, uploadResp)

	exportResp, err := client.Get(baseURL + "/api/auth/me/export")
	if err != nil {
		t.Fatalf("GET /api/auth/me/export: %v", err)
	}
	if exportResp.StatusCode != http.StatusOK {
		t.Fatalf("export: want 200, got %d: %s", exportResp.StatusCode, bodyString(t, exportResp))
	}
	export := decodeJSONMap(t, exportResp)
	files, _ := export["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("want 1 file in export, got %v", export["files"])
	}
	got, _ := files[0].(map[string]any)
	if got["id"] != uploaded["id"] {
		t.Fatalf("want exported file id %v, got %v", uploaded["id"], got["id"])
	}
}

func TestStorage_AccountDeletionErasesFiles(t *testing.T) {
	// STOR-08
	baseURL := testserver.New(t)
	client := newClient(t)
	signUp(t, client, baseURL, "erase@example.com", "correcthorse")

	clientID := registerPublicClient(t, baseURL, testRedirectURI, "files:read files:write")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"files:read", "files:write"})
	at, _ := tok["access_token"].(string)
	if at == "" {
		t.Fatalf("setup: no access_token in %v", tok)
	}

	uploadResp := uploadFile(t, baseURL, at, "erase.txt", []byte("data"))
	created := decodeJSONMap(t, uploadResp)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("upload: no id in %v", created)
	}

	delResp, err := doDeleteWithClient(t, client, baseURL+"/api/auth/me")
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete account: want 204, got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	// The access token's underlying grant is revoked by account deletion
	// (see spec/identity.md IDNT-13), so a 401 here would also prove
	// erasure; asserting 404-or-401 keeps this test from being coupled to
	// which layer rejects first. Either way, the file is unreachable.
	getResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id, at, nil)
	if getResp.StatusCode != http.StatusNotFound && getResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("get after account deletion: want 404 or 401, got %d", getResp.StatusCode)
	}
}

// doDeleteWithClient issues DELETE using client (so the request carries
// its session cookie) rather than a bearer token.
func doDeleteWithClient(t *testing.T, client *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

// failingBackend wraps a real storage.Backend and makes Delete fail for
// exactly one key (failKey, mutable after construction so a test can
// upload files first — getting real, server-generated ids — before
// deciding which one to fail) while every other operation, and Delete
// for every other key, passes straight through.
type failingBackend struct {
	inner   storage.Backend
	failKey string
}

func (b *failingBackend) Put(ctx context.Context, key string, r io.Reader) (int64, error) {
	return b.inner.Put(ctx, key, r)
}
func (b *failingBackend) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.inner.Open(ctx, key)
}
func (b *failingBackend) Delete(ctx context.Context, key string) error {
	if key == b.failKey {
		return fmt.Errorf("simulated delete failure for %s", key)
	}
	return b.inner.Delete(ctx, key)
}

// TestStorage_PartialDeleteOwnedFailureLeavesConsistentState is STOR-13:
// given 3 uploaded files where the 2nd's byte deletion fails, DeleteOwned
// must leave the 1st (processed before the failure, in upload order —
// see DeleteOwned's ORDER BY) fully cleaned, the 2nd's metadata still
// present (so it's discoverable and retryable, not orphaned pointing at
// bytes that silently vanished), and the 3rd (never reached) completely
// untouched. Before this, a bulk "delete all bytes in a loop, then bulk-
// delete all metadata at the end" ordering meant a failure here left
// every file's metadata in place regardless of which bytes were already
// gone.
func TestStorage_PartialDeleteOwnedFailureLeavesConsistentState(t *testing.T) {
	inner, err := storage.NewLocalBackend(t.TempDir())
	if err != nil {
		t.Fatalf("init local backend: %v", err)
	}
	fake := &failingBackend{inner: inner}
	baseURL := testserver.NewCustom(t, testserver.Options{StorageBackend: fake})

	client := newClient(t)
	signUp(t, client, baseURL, "partial-delete@example.com", "correcthorse")
	clientID := registerPublicClient(t, baseURL, testRedirectURI, "files:read files:write")
	tok := authorizeAndGetToken(t, client, baseURL, clientID, testRedirectURI, []string{"files:read", "files:write"})
	at, _ := tok["access_token"].(string)

	firstID, _ := decodeJSONMap(t, uploadFile(t, baseURL, at, "first.txt", []byte("1")))["id"].(string)
	secondID, _ := decodeJSONMap(t, uploadFile(t, baseURL, at, "second.txt", []byte("2")))["id"].(string)
	thirdID, _ := decodeJSONMap(t, uploadFile(t, baseURL, at, "third.txt", []byte("3")))["id"].(string)
	if firstID == "" || secondID == "" || thirdID == "" {
		t.Fatalf("setup: want 3 uploaded files with ids, got %q %q %q", firstID, secondID, thirdID)
	}

	fake.failKey = secondID

	delResp, err := doDeleteWithClient(t, client, baseURL+"/api/auth/me")
	if err != nil {
		t.Fatalf("DELETE /api/auth/me: %v", err)
	}
	if delResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500 (the simulated partial failure), got %d: %s", delResp.StatusCode, bodyString(t, delResp))
	}

	firstGet := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+firstID, at, nil)
	if firstGet.StatusCode != http.StatusNotFound {
		t.Fatalf("first file (processed before the failure): want 404 (fully cleaned), got %d", firstGet.StatusCode)
	}

	secondGet := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+secondID, at, nil)
	if secondGet.StatusCode != http.StatusOK {
		t.Fatalf("second file (bytes delete failed): want 200 metadata (still discoverable), got %d", secondGet.StatusCode)
	}
	secondContent := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+secondID+"/content", at, nil)
	if secondContent.StatusCode != http.StatusOK {
		t.Fatalf("second file content (bytes never actually removed): want 200, got %d", secondContent.StatusCode)
	}

	thirdGet := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+thirdID, at, nil)
	if thirdGet.StatusCode != http.StatusOK {
		t.Fatalf("third file (never reached): want 200, got %d", thirdGet.StatusCode)
	}

	// A retry, once the transient failure clears, converges to fully
	// erased instead of leaving anything orphaned forever.
	fake.failKey = ""
	retryResp, err := doDeleteWithClient(t, client, baseURL+"/api/auth/me")
	if err != nil {
		t.Fatalf("retry DELETE /api/auth/me: %v", err)
	}
	if retryResp.StatusCode != http.StatusNoContent {
		t.Fatalf("retry: want 204, got %d: %s", retryResp.StatusCode, bodyString(t, retryResp))
	}
}

func TestStorage_OversizedUploadRejected(t *testing.T) {
	// STOR-09
	baseURL := testserver.NewCustom(t, testserver.Options{MaxUploadBytes: 200})
	token := storageToken(t, baseURL, "stor09@example.com", []string{"files:read", "files:write"})

	resp := uploadFile(t, baseURL, token, "big.bin", bytes.Repeat([]byte("a"), 1000))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized upload: want 400, got %d: %s", resp.StatusCode, bodyString(t, resp))
	}

	listResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files", token, nil)
	list := decodeJSONMap(t, listResp)
	if records, _ := list["records"].([]any); len(records) != 0 {
		t.Fatalf("want no file row created by the rejected upload, got %v", records)
	}
}

func TestStorage_MalformedMultipartRejectedNot500(t *testing.T) {
	// STOR-10
	baseURL := testserver.New(t)
	token := storageToken(t, baseURL, "stor10@example.com", []string{"files:read", "files:write"})

	postMultipart := func(t *testing.T, contentType string, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/storage/files", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST /api/storage/files: %v", err)
		}
		return resp
	}

	// Not valid multipart at all, despite the header claiming it is.
	garbage := postMultipart(t, "multipart/form-data; boundary=xyz", []byte("this is not multipart data"))
	if garbage.StatusCode != http.StatusBadRequest {
		t.Fatalf("garbage multipart body: want 400, got %d: %s", garbage.StatusCode, bodyString(t, garbage))
	}

	// Valid multipart, but no "file" field — a different field entirely.
	var wrongFieldBody bytes.Buffer
	mw := multipart.NewWriter(&wrongFieldBody)
	fw, err := mw.CreateFormField("not_file")
	if err != nil {
		t.Fatalf("create form field: %v", err)
	}
	if _, err := fw.Write([]byte("hello")); err != nil {
		t.Fatalf("write form field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	wrongField := postMultipart(t, mw.FormDataContentType(), wrongFieldBody.Bytes())
	if wrongField.StatusCode != http.StatusBadRequest {
		t.Fatalf("multipart missing the file field: want 400, got %d: %s", wrongField.StatusCode, bodyString(t, wrongField))
	}
}

func TestStorage_ZeroByteFileUploadsAndDownloads(t *testing.T) {
	// STOR-11
	baseURL := testserver.New(t)
	token := storageToken(t, baseURL, "stor11@example.com", []string{"files:read", "files:write"})

	uploadResp := uploadFile(t, baseURL, token, "empty.txt", []byte{})
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("zero-byte upload: want 201, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
	}
	created := decodeJSONMap(t, uploadResp)
	if size, _ := created["size_bytes"].(float64); size != 0 {
		t.Fatalf("want size_bytes 0, got %v", created["size_bytes"])
	}
	id, _ := created["id"].(string)

	contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id+"/content", token, nil)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("download zero-byte file: want 200, got %d", contentResp.StatusCode)
	}
	body, err := io.ReadAll(contentResp.Body)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("want an empty body, got %d bytes", len(body))
	}
}

func TestStorage_AdversarialFilenameContentDispositionIsWellFormed(t *testing.T) {
	// STOR-12
	baseURL := testserver.New(t)
	token := storageToken(t, baseURL, "stor12@example.com", []string{"files:read", "files:write"})

	for _, tc := range []struct {
		name, filename string
		want           string // substring the Content-Disposition header must contain
	}{
		{"quote", `with"quote.txt`, `filename="with\"quote.txt"`},
		{"backslash", `with\backslash.txt`, `filename="with\\backslash.txt"`},
		{"non-ascii", "日本語.txt", "filename*=utf-8''%E6%97%A5%E6%9C%AC%E8%AA%9E.txt"},
		{"emoji", "emoji😀.txt", "filename*=utf-8''emoji%F0%9F%98%80.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uploadResp := uploadFile(t, baseURL, token, tc.filename, []byte("x"))
			if uploadResp.StatusCode != http.StatusCreated {
				t.Fatalf("upload: want 201, got %d: %s", uploadResp.StatusCode, bodyString(t, uploadResp))
			}
			id, _ := decodeJSONMap(t, uploadResp)["id"].(string)

			contentResp := doAuthed(t, http.MethodGet, baseURL+"/api/storage/files/"+id+"/content", token, nil)
			if contentResp.StatusCode != http.StatusOK {
				t.Fatalf("download: want 200, got %d", contentResp.StatusCode)
			}
			cd := contentResp.Header.Get("Content-Disposition")
			if !strings.HasPrefix(cd, "attachment") {
				t.Fatalf("want a well-formed attachment Content-Disposition, got %q", cd)
			}
			if !strings.Contains(cd, tc.want) {
				t.Fatalf("want Content-Disposition to contain %q, got %q", tc.want, cd)
			}
			// Never the raw, unescaped filename leaking through unquoted —
			// that's exactly what a naive fmt.Sprintf(`filename=%q`, ...)
			// used to do for non-ASCII text.
			if strings.Contains(cd, tc.filename) && tc.name != "quote" && tc.name != "backslash" {
				t.Fatalf("want the adversarial filename properly encoded, not embedded raw, got %q", cd)
			}
		})
	}
}
