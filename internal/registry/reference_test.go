package registry

import "testing"

func TestParseReference(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name       string
		input      string
		registry   string
		repository string
		tag        string
		digest     string
	}{
		{"hub namespace", "library/nginx:latest", dockerHubRegistry, "library/nginx", "latest", ""},
		{"hub library", "ubuntu", dockerHubRegistry, "library/ubuntu", "latest", ""},
		{"hub alias", "docker.io/library/alpine:3.20", dockerHubRegistry, "library/alpine", "3.20", ""},
		{"custom registry", "registry.example:5443/team/image:v1", "registry.example:5443", "team/image", "v1", ""},
		{"digest", "library/nginx@" + digest, dockerHubRegistry, "library/nginx", "", digest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reference, err := ParseReference(test.input)
			if err != nil {
				t.Fatalf("ParseReference: %v", err)
			}
			if reference.Registry != test.registry || reference.Repository != test.repository || reference.Tag != test.tag || reference.Digest != test.digest {
				t.Fatalf("unexpected reference: %#v", reference)
			}
		})
	}
}

func TestParseReferenceRejectsUnsafeValues(t *testing.T) {
	for _, value := range []string{
		"",
		" ubuntu",
		"http://registry.example/image:tag",
		"UPPER/image:tag",
		"registry.example/team/../image:tag",
		"registry.example/team/image:",
		"ubuntu@sha256:abc",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseReference(value); err == nil {
				t.Fatalf("ParseReference(%q) unexpectedly succeeded", value)
			}
		})
	}
}

func TestReferenceValidateRejectsNonNormalizedConstruction(t *testing.T) {
	for _, reference := range []Reference{
		{Registry: "registry.example", Repository: "team/image"},
		{Registry: "registry.example/path", Repository: "team/image", Tag: "latest"},
		{Registry: dockerHubRegistry, Repository: "ubuntu", Tag: "latest"},
	} {
		if err := reference.Validate(); err == nil {
			t.Fatalf("Reference.Validate(%#v) unexpectedly succeeded", reference)
		}
	}
}
