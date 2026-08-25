package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const dockerHubRegistry = "registry-1.docker.io"

var (
	repositoryComponentPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*$`)
	tagPattern                 = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestPattern              = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

func (reference Reference) Validate() error {
	parsed, err := ParseReference(reference.String())
	if err != nil {
		return err
	}
	if parsed != reference {
		return fmt.Errorf("image reference is incomplete or not normalized")
	}
	return nil
}

func ParseReference(value string) (Reference, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return Reference{}, fmt.Errorf("image reference must be non-empty and contain no surrounding whitespace")
	}
	if strings.Contains(value, "://") {
		return Reference{}, fmt.Errorf("image reference must not contain a URL scheme")
	}
	if strings.Count(value, "@") > 1 {
		return Reference{}, fmt.Errorf("image reference contains more than one digest separator")
	}

	nameAndTag, digest, hasDigest := strings.Cut(value, "@")
	if hasDigest {
		digest = strings.ToLower(digest)
		if err := ValidateDigest(digest); err != nil {
			return Reference{}, err
		}
	}
	if nameAndTag == "" {
		return Reference{}, fmt.Errorf("image repository is empty")
	}

	tag := ""
	lastSlash := strings.LastIndexByte(nameAndTag, '/')
	lastColon := strings.LastIndexByte(nameAndTag, ':')
	if lastColon > lastSlash {
		tag = nameAndTag[lastColon+1:]
		nameAndTag = nameAndTag[:lastColon]
		if !tagPattern.MatchString(tag) {
			return Reference{}, fmt.Errorf("invalid image tag %q", tag)
		}
	}
	if !hasDigest && tag == "" {
		tag = "latest"
	}

	registryHost := dockerHubRegistry
	repository := nameAndTag
	if slash := strings.IndexByte(nameAndTag, '/'); slash >= 0 {
		candidate := nameAndTag[:slash]
		if isRegistryComponent(candidate) {
			registryHost = strings.ToLower(candidate)
			repository = nameAndTag[slash+1:]
		}
	}
	if registryHost == "docker.io" || registryHost == "index.docker.io" {
		registryHost = dockerHubRegistry
	}
	if err := validateRegistryHost(registryHost); err != nil {
		return Reference{}, err
	}
	if repository == "" || strings.ToLower(repository) != repository {
		return Reference{}, fmt.Errorf("image repository must be non-empty and lowercase")
	}
	components := strings.Split(repository, "/")
	for _, component := range components {
		if !repositoryComponentPattern.MatchString(component) {
			return Reference{}, fmt.Errorf("invalid image repository component %q", component)
		}
	}
	if registryHost == dockerHubRegistry && len(components) == 1 {
		repository = "library/" + repository
	}

	return Reference{
		Registry:   registryHost,
		Repository: repository,
		Tag:        tag,
		Digest:     digest,
	}, nil
}

func ValidateDigest(value string) error {
	if !digestPattern.MatchString(strings.ToLower(value)) {
		return fmt.Errorf("invalid sha256 manifest digest %q", value)
	}
	return nil
}

func (reference Reference) ManifestReference() string {
	if reference.Digest != "" {
		return reference.Digest
	}
	return reference.Tag
}

func (reference Reference) SameRepository(other Reference) bool {
	return reference.Registry == other.Registry && reference.Repository == other.Repository
}

func (reference Reference) String() string {
	name := reference.Registry + "/" + reference.Repository
	if reference.Tag != "" {
		name += ":" + reference.Tag
	}
	if reference.Digest != "" {
		name += "@" + reference.Digest
	}
	return name
}

func isRegistryComponent(value string) bool {
	return value == "localhost" || strings.ContainsAny(value, ".:") || strings.HasPrefix(value, "[")
}

func validateRegistryHost(host string) error {
	parsed, err := url.Parse("https://" + host)
	if err != nil || parsed.Host != host || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid registry host %q", host)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("invalid registry host %q", host)
	}
	return nil
}
