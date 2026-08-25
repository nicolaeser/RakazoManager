package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxManifestBody = int64(8 << 20)
	maxTokenBody    = int64(1 << 20)
	maxErrorBody    = int64(64 << 10)
	maxBearerToken  = 64 << 10
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json"

type DigestResolver interface {
	ManifestDigest(context.Context, Reference) (string, error)
}

type Client struct {
	httpClient *http.Client

	Auth AuthProvider
}

func NewClient(source *http.Client) *Client {
	if source == nil {
		source = &http.Client{Timeout: 30 * time.Second}
	}
	cloned := *source
	if cloned.Timeout == 0 {
		cloned.Timeout = 30 * time.Second
	}
	originalRedirect := cloned.CheckRedirect
	cloned.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" {
			return fmt.Errorf("refusing registry redirect to non-HTTPS URL")
		}
		if len(via) > 0 && !strings.EqualFold(request.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("refusing cross-host registry redirect from %s to %s", via[0].URL.Host, request.URL.Host)
		}
		if originalRedirect != nil {
			return originalRedirect(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &Client{httpClient: &cloned, Auth: DockerConfigAuth{}}
}

func (client *Client) ManifestDigest(ctx context.Context, reference Reference) (string, error) {
	if client == nil || client.httpClient == nil {
		return "", fmt.Errorf("registry HTTP client is not configured")
	}
	if err := reference.Validate(); err != nil {
		return "", fmt.Errorf("invalid registry reference: %w", err)
	}
	address := manifestURL(reference)
	auth, authErr := client.registryAuth(reference.Registry)
	if authErr != nil {
		return "", authErr
	}
	response, err := client.requestManifest(ctx, address, "", auth, false)
	if err != nil {
		return "", err
	}
	if response.StatusCode == http.StatusUnauthorized {
		challenge := response.Header.Get("WWW-Authenticate")
		if err := discardBounded(response.Body, maxErrorBody); err != nil {
			return "", fmt.Errorf("read registry authentication response: %w", err)
		}
		if bearer, ok := extractBearerChallenge(challenge); ok {
			token, tokenErr := client.bearerToken(ctx, bearer, reference, auth)
			if tokenErr != nil {
				return "", tokenErr
			}
			response, err = client.requestManifest(ctx, address, token, Auth{}, false)
			if err != nil {
				return "", err
			}
		} else if auth.Username != "" || auth.Password != "" {
			response, err = client.requestManifest(ctx, address, "", auth, true)
			if err != nil {
				return "", err
			}
		} else {
			return "", fmt.Errorf("registry authentication required and no usable credentials were found in Docker config auths")
		}
	}
	return verifiedManifestDigest(response, address, reference.Digest)
}

func (client *Client) registryAuth(registryHost string) (Auth, error) {
	if client.Auth == nil {
		return Auth{}, nil
	}
	auth, ok, err := client.Auth.AuthFor(registryHost)
	if err != nil {
		return Auth{}, fmt.Errorf("resolve registry credentials: %w", err)
	}
	if !ok {
		return Auth{}, nil
	}
	return auth, nil
}

func (client *Client) requestManifest(ctx context.Context, address, token string, auth Auth, forceBasic bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", manifestAccept)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "RakazoManager-registry-check")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	} else if forceBasic && (auth.Username != "" || auth.Password != "") {
		request.SetBasicAuth(auth.Username, auth.Password)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request registry manifest: %w", err)
	}
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" {
		_ = response.Body.Close()
		return nil, fmt.Errorf("registry request was redirected away from HTTPS")
	}
	return response, nil
}

func (client *Client) bearerToken(ctx context.Context, header string, reference Reference, auth Auth) (string, error) {
	parameters, err := parseBearerChallenge(header)
	if err != nil {
		return "", err
	}
	realm, err := url.Parse(parameters["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" || realm.User != nil {
		return "", fmt.Errorf("refusing invalid or non-HTTPS bearer realm %q", parameters["realm"])
	}
	if !allowedBearerRealm(reference.Registry, realm) {
		return "", fmt.Errorf("refusing bearer realm %q outside the configured registry trust boundary", realm.Host)
	}
	query := realm.Query()
	if service := parameters["service"]; service != "" {
		query.Set("service", service)
	}
	scope := parameters["scope"]
	if scope == "" {
		scope = "repository:" + reference.Repository + ":pull"
	}
	query.Set("scope", scope)
	realm.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "RakazoManager-registry-check")
	if auth.Username != "" || auth.Password != "" {
		request.SetBasicAuth(auth.Username, auth.Password)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("request registry bearer token: %w", err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" {
		return "", fmt.Errorf("bearer token request was redirected away from HTTPS")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, readErr := readBounded(response.Body, maxErrorBody)
		if readErr != nil {
			return "", fmt.Errorf("bearer token endpoint returned HTTP %d", response.StatusCode)
		}
		return "", fmt.Errorf("bearer token endpoint returned HTTP %d: %s", response.StatusCode, diagnosticExcerpt(detail))
	}
	body, err := readBounded(response.Body, maxTokenBody)
	if err != nil {
		return "", fmt.Errorf("read registry bearer token: %w", err)
	}
	var tokenResponse struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&tokenResponse); err != nil {
		return "", fmt.Errorf("decode registry bearer token: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("decode registry bearer token: trailing content")
	}
	token := tokenResponse.Token
	if token == "" {
		token = tokenResponse.AccessToken
	}
	if !validBearerToken(token) {
		return "", fmt.Errorf("registry bearer token is empty or invalid")
	}
	return token, nil
}

func validBearerToken(token string) bool {
	if token == "" || len(token) > maxBearerToken {
		return false
	}
	padding := false
	for _, character := range token {
		if character == '=' {
			padding = true
			continue
		}
		if padding {
			return false
		}
		allowed := character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("-._~+/", character)
		if !allowed {
			return false
		}
	}
	return true
}

func allowedBearerRealm(registryHost string, realm *url.URL) bool {
	if strings.EqualFold(realm.Host, registryHost) {
		return true
	}
	return registryHost == dockerHubRegistry && strings.EqualFold(realm.Host, "auth.docker.io")
}

func verifiedManifestDigest(response *http.Response, address, expectedDigest string) (string, error) {
	defer response.Body.Close()
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return "", fmt.Errorf("registry manifest used unsupported content encoding %q", encoding)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, err := readBounded(response.Body, maxErrorBody)
		if err != nil {
			return "", fmt.Errorf("registry manifest returned HTTP %d", response.StatusCode)
		}
		return "", fmt.Errorf("registry manifest returned HTTP %d for %s: %s", response.StatusCode, address, diagnosticExcerpt(detail))
	}
	body, err := readBounded(response.Body, maxManifestBody)
	if err != nil {
		return "", fmt.Errorf("read registry manifest: %w", err)
	}
	digest := strings.ToLower(strings.TrimSpace(response.Header.Get("Docker-Content-Digest")))
	if err := ValidateDigest(digest); err != nil {
		return "", fmt.Errorf("registry returned invalid Docker-Content-Digest: %w", err)
	}
	if expectedDigest != "" && digest != expectedDigest {
		return "", fmt.Errorf("registry returned digest %s for digest-addressed request %s", digest, expectedDigest)
	}
	actual := sha256.Sum256(body)
	if "sha256:"+hex.EncodeToString(actual[:]) != digest {
		return "", fmt.Errorf("registry manifest body does not match Docker-Content-Digest")
	}
	return digest, nil
}

func diagnosticExcerpt(detail []byte) string {
	const maxDiagnosticBytes = 512
	truncated := len(detail) > maxDiagnosticBytes
	if truncated {
		detail = detail[:maxDiagnosticBytes]
	}
	text := strconv.QuoteToASCII(strings.TrimSpace(string(detail)))
	if truncated {
		text += " (truncated)"
	}
	return text
}

func manifestURL(reference Reference) string {
	segments := strings.Split(reference.Repository, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return "https://" + reference.Registry + "/v2/" + strings.Join(segments, "/") + "/manifests/" + url.PathEscape(reference.ManifestReference())
}

func extractBearerChallenge(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}

	if len(header) >= 7 && strings.EqualFold(header[:6], "Bearer") && (header[6] == ' ' || header[6] == '\t') {
		return header, true
	}

	lower := strings.ToLower(header)
	index := strings.Index(lower, "bearer ")
	if index < 0 {
		index = strings.Index(lower, "bearer\t")
	}
	if index < 0 {
		return "", false
	}
	return header[index:], true
}

func parseBearerChallenge(header string) (map[string]string, error) {
	header = strings.TrimSpace(header)
	if extracted, ok := extractBearerChallenge(header); ok {
		header = extracted
	}
	const scheme = "Bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return nil, fmt.Errorf("registry did not return a supported Bearer challenge")
	}
	parameters := map[string]string{}
	remainder := strings.TrimSpace(header[len(scheme):])
	for remainder != "" {

		if nextSchemeStart(remainder) {
			break
		}
		equals := strings.IndexByte(remainder, '=')
		if equals <= 0 {
			return nil, fmt.Errorf("invalid registry Bearer challenge")
		}
		key := strings.ToLower(strings.TrimSpace(remainder[:equals]))
		remainder = strings.TrimSpace(remainder[equals+1:])
		if remainder == "" {
			return nil, fmt.Errorf("invalid registry Bearer challenge value for %s", key)
		}
		var value string
		var rest string
		var err error
		if remainder[0] == '"' {
			value, rest, err = consumeQuoted(remainder)
			if err != nil {
				return nil, err
			}
		} else {

			end := len(remainder)
			for i := 0; i < len(remainder); i++ {
				if remainder[i] == ',' || remainder[i] == ' ' || remainder[i] == '\t' {
					end = i
					break
				}
			}
			value = remainder[:end]
			rest = remainder[end:]
		}
		if key == "" || parameters[key] != "" {
			return nil, fmt.Errorf("invalid duplicate or empty registry Bearer challenge key")
		}
		parameters[key] = value
		remainder = strings.TrimSpace(rest)
		if remainder == "" {
			break
		}
		if remainder[0] != ',' {

			if nextSchemeStart(remainder) {
				break
			}
			return nil, fmt.Errorf("invalid registry Bearer challenge separator")
		}
		remainder = strings.TrimSpace(remainder[1:])
	}
	if parameters["realm"] == "" {
		return nil, fmt.Errorf("registry Bearer challenge has no realm")
	}
	return parameters, nil
}

func nextSchemeStart(value string) bool {

	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	space := strings.IndexAny(trimmed, " \t")
	token := trimmed
	if space > 0 {
		token = trimmed[:space]
	}
	switch strings.ToLower(token) {
	case "basic", "digest", "bearer":

		return true
	default:
		return false
	}
}

func consumeQuoted(value string) (string, string, error) {
	var output strings.Builder
	escaped := false
	for index := 1; index < len(value); index++ {
		character := value[index]
		if escaped {
			output.WriteByte(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == '"' {
			return output.String(), value[index+1:], nil
		}
		if character == '\r' || character == '\n' || character == 0 {
			return "", "", fmt.Errorf("invalid control character in registry Bearer challenge")
		}
		output.WriteByte(character)
	}
	return "", "", fmt.Errorf("unterminated registry Bearer challenge value")
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeded %d-byte limit", limit)
	}
	return body, nil
}

func discardBounded(reader io.ReadCloser, limit int64) error {
	defer reader.Close()
	_, err := readBounded(reader, limit)
	return err
}
