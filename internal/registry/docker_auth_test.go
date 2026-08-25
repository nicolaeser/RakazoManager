package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerConfigAuthReadsBase64Auth(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	encoded := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	content := fmt.Sprintf(`{"auths":{"registry.example.com":{"auth":%q}}}`, encoded)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, ok, err := (DockerConfigAuth{Path: path}).AuthFor("registry.example.com")
	if err != nil || !ok {
		t.Fatalf("AuthFor: ok=%v err=%v", ok, err)
	}
	if auth.Username != "alice" || auth.Password != "s3cret" {
		t.Fatalf("unexpected auth %#v", auth)
	}
}

func TestManifestDigestUsesDockerConfigForBearerToken(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"mediaType":"private"}`)
	sum := sha256.Sum256(manifest)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var sawBasic bool
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == "/token":
			username, password, ok := request.BasicAuth()
			if !ok || username != "alice" || password != "s3cret" {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			sawBasic = true
			_, _ = writer.Write([]byte(`{"token":"aaaa.bbbb.cccc"}`))
		case strings.HasSuffix(request.URL.Path, "/manifests/latest"):
			if request.Header.Get("Authorization") == "" {
				writer.Header().Set("WWW-Authenticate", fmt.Sprintf(`Basic realm="registry", Bearer realm=%q,service="registry",scope="repository:team/image:pull"`, server.URL+"/token"))
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			if request.Header.Get("Authorization") != "Bearer aaaa.bbbb.cccc" {
				http.Error(writer, "bad token", http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Docker-Content-Digest", digest)
			_, _ = writer.Write(manifest)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	host := strings.TrimPrefix(server.URL, "https://")
	encoded := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host, encoded)), 0o600); err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client())
	client.Auth = DockerConfigAuth{Path: configPath}
	reference := tlsServerReference(t, server.URL, "team/image", "latest")
	got, err := client.ManifestDigest(context.Background(), reference)
	if err != nil {
		t.Fatalf("ManifestDigest: %v", err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
	if !sawBasic {
		t.Fatal("expected basic auth on token endpoint")
	}
}

func TestExtractBearerChallengeFromMultiSchemeHeader(t *testing.T) {
	header := `Basic realm="registry",Bearer realm="https://auth.example/token",service="registry"`
	got, ok := extractBearerChallenge(header)
	if !ok || !strings.HasPrefix(strings.ToLower(got), "bearer ") {
		t.Fatalf("extractBearerChallenge = %q ok=%v", got, ok)
	}
	params, err := parseBearerChallenge(header)
	if err != nil {
		t.Fatal(err)
	}
	if params["realm"] != "https://auth.example/token" || params["service"] != "registry" {
		t.Fatalf("params = %#v", params)
	}
}
