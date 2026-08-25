package config

import "testing"

func TestNormalizeOrigin(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "  rakazo.example.com  ", want: "https://rakazo.example.com"},
		{in: "https://rakazo.example.com", want: "https://rakazo.example.com"},
		{in: "http://127.0.0.1:5173", want: "http://127.0.0.1:5173"},
		{in: "https://rakazo.example.com:8443", want: "https://rakazo.example.com:8443"},
		{in: "https://rakazo.example.com/", want: "https://rakazo.example.com"},
		{in: "https://rakazo.example.com/app", wantErr: true},
		{in: "ftp://rakazo.example.com", wantErr: true},
		{in: "0.0.0.0", wantErr: true},
		{in: "https://user:pass@rakazo.example.com", wantErr: true},
	}
	for _, test := range tests {
		got, err := NormalizeOrigin(test.in)
		if test.wantErr {
			if err == nil {
				t.Errorf("NormalizeOrigin(%q) = %q, want error", test.in, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", test.in, got, err, test.want)
		}
	}
}

func TestResolveOriginTurnsIPIntoHTTPWithWebPort(t *testing.T) {
	got, err := ResolveOrigin("10.0.10.3", 5190)
	if err != nil || got != "http://10.0.10.3:5190" {
		t.Fatalf("got %q, %v", got, err)
	}
	got, err = ResolveOrigin("10.0.10.3:8080", 5190)
	if err != nil || got != "http://10.0.10.3:8080" {
		t.Fatalf("explicit IP port: got %q, %v", got, err)
	}
	got, err = ResolveOrigin("rakazo.example.com", 5190)
	if err != nil || got != "https://rakazo.example.com" {
		t.Fatalf("domain: got %q, %v", got, err)
	}
	got, err = ResolveOrigin("https://10.0.10.3", 5190)
	if err != nil || got != "https://10.0.10.3" {
		t.Fatalf("explicit https IP: got %q, %v", got, err)
	}
	if _, err := ResolveOrigin("10.0.10.3", 0); err == nil {
		t.Fatal("IP without web port should fail")
	}
}

func TestPublicOriginUsesConfiguredOrigin(t *testing.T) {
	cfg := New("/tmp/stack", "rakazo-test", DefaultImage, 5173, 3100, 5433)
	if cfg.PublicOrigin() != "http://127.0.0.1:5173" {
		t.Fatalf("default origin: %s", cfg.PublicOrigin())
	}
	cfg.Origin = "https://rakazo.example.com"
	if cfg.PublicOrigin() != "https://rakazo.example.com" {
		t.Fatalf("configured origin: %s", cfg.PublicOrigin())
	}
}
