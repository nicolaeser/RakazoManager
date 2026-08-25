package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManifestDigestWithoutAuthentication(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2}`)
	digest := digestOf(manifest)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/team/image/manifests/latest" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		if !strings.Contains(request.Header.Get("Accept"), "application/vnd.oci.image.manifest.v1+json") {
			t.Errorf("manifest Accept header missing: %q", request.Header.Get("Accept"))
		}
		if request.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("manifest Accept-Encoding = %q", request.Header.Get("Accept-Encoding"))
		}
		writer.Header().Set("Docker-Content-Digest", digest)
		_, _ = writer.Write(manifest)
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	got, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err != nil {
		t.Fatalf("ManifestDigest: %v", err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
}

func TestManifestDigestFollowsHTTPSBearerChallenge(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"mediaType":"test"}`)
	digest := digestOf(manifest)
	var manifestRequests atomic.Int32
	var tokenRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			tokenRequests.Add(1)
			if request.URL.Query().Get("service") != "registry.test" {
				t.Errorf("service = %q", request.URL.Query().Get("service"))
			}
			if request.URL.Query().Get("scope") != "repository:team/image:pull" {
				t.Errorf("scope = %q", request.URL.Query().Get("scope"))
			}
			_, _ = writer.Write([]byte(`{"token":"test-token","expires_in":300,"issued_at":"2026-08-11T10:00:00Z"}`))
		case "/v2/team/image/manifests/stable":
			manifestRequests.Add(1)
			if request.Header.Get("Authorization") != "Bearer test-token" {
				writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="registry.test",scope="repository:team/image:pull"`, server.URL+"/token"))
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Docker-Content-Digest", digest)
			_, _ = writer.Write(manifest)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "stable")
	got, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err != nil {
		t.Fatalf("ManifestDigest: %v", err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
	if manifestRequests.Load() != 2 || tokenRequests.Load() != 1 {
		t.Fatalf("requests: manifest=%d token=%d", manifestRequests.Load(), tokenRequests.Load())
	}
}

func TestManifestDigestRejectsNonHTTPSBearerRealm(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="http://auth.example/token"`)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS bearer realm") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestRejectsInvalidBearerToken(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			_, _ = writer.Write([]byte(`{"token":"invalid token"}`))
			return
		}
		writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, server.URL+"/token"))
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "token is empty or invalid") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestBoundsTokenResponse(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			_, _ = writer.Write([]byte(`{"token":"` + strings.Repeat("x", int(maxTokenBody)) + `"}`))
			return
		}
		writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, server.URL+"/token"))
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestRejectsDigestMismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		_, _ = writer.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestRejectsUnexpectedContentEncoding(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Encoding", "gzip")
		writer.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		_, _ = writer.Write([]byte("not actually gzip"))
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "unsupported content encoding") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestRejectsSubstitutionForDigestAddress(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2}`)
	returnedDigest := digestOf(manifest)
	expectedDigest := "sha256:" + strings.Repeat("a", 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Docker-Content-Digest", returnedDigest)
		_, _ = writer.Write(manifest)
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "")
	reference.Digest = expectedDigest
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "digest-addressed request") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestDigestRejectsBearerRealmOutsideTrustBoundary(t *testing.T) {
	var tokenRequests atomic.Int32
	tokenServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests.Add(1)
		_, _ = writer.Write([]byte(`{"token":"must-not-be-read"}`))
	}))
	defer tokenServer.Close()
	manifestServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, tokenServer.URL))
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer manifestServer.Close()

	reference := tlsServerReference(t, manifestServer.URL, "team/image", "latest")
	_, err := NewClient(manifestServer.Client()).ManifestDigest(context.Background(), reference)
	if err == nil || !strings.Contains(err.Error(), "trust boundary") {
		t.Fatalf("error = %v", err)
	}
	if tokenRequests.Load() != 0 {
		t.Fatalf("untrusted bearer realm received %d request(s)", tokenRequests.Load())
	}
}

func TestManifestErrorDetailIsEscapedAndTruncated(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("\x1b[31m" + strings.Repeat("x", 1024)))
	}))
	defer server.Close()

	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	_, err := NewClient(server.Client()).ManifestDigest(context.Background(), reference)
	if err == nil {
		t.Fatal("expected manifest error")
	}
	if strings.ContainsRune(err.Error(), '\x1b') || !strings.Contains(err.Error(), `\x1b`) || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("unsafe or unbounded error = %q", err.Error())
	}
}

func TestNewClientBoundsInjectedClientWithoutMutatingIt(t *testing.T) {
	source := &http.Client{}
	client := NewClient(source)
	if source.Timeout != 0 {
		t.Fatalf("source client was mutated: %s", source.Timeout)
	}
	if client.httpClient.Timeout != 30*time.Second {
		t.Fatalf("cloned client timeout = %s", client.httpClient.Timeout)
	}
}

func tlsServerReference(t *testing.T, serverURL, repository, tag string) Reference {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return Reference{Registry: parsed.Host, Repository: repository, Tag: tag}
}

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
