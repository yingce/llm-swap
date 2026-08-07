package config

import (
	"strings"
	"testing"
)

func TestModelDirIdentityForOSMatchesFilesystemCaseSemantics(t *testing.T) {
	if upper, lower := ModelDirIdentityForOS("A-Pro", "windows"), ModelDirIdentityForOS("a-pro", "windows"); upper != lower {
		t.Fatalf("Windows identities = %q and %q, want equal", upper, lower)
	}
	if upper, lower := ModelDirIdentityForOS("A-Pro", "linux"), ModelDirIdentityForOS("a-pro", "linux"); upper == lower {
		t.Fatalf("Linux identities = %q and %q, want distinct", upper, lower)
	}
	if got, want := ModelDirIdentityForOS(`Family\tmp\..\V1`, "windows"), ModelDirIdentityForOS("family/v1", "windows"); got != want {
		t.Fatalf("path-equivalent Windows identities = %q and %q, want equal", got, want)
	}
}

func TestValidateModelIdentitiesForOSRejectsFilesystemIdentityCollisions(t *testing.T) {
	cfg := GatewayConfig{Models: map[string]Model{
		"A-Pro": {},
		"a-pro": {},
	}}

	if err := validateModelIdentitiesForOS(cfg, "linux"); err != nil {
		t.Fatalf("Linux validation rejected case-distinct directories: %v", err)
	}
	err := validateModelIdentitiesForOS(cfg, "windows")
	if err == nil || !strings.Contains(err.Error(), "duplicate model_dir") {
		t.Fatalf("Windows validation error = %v, want duplicate model_dir", err)
	}
}

func TestValidateModelIdentitiesForOSRejectsReservedDirectoryIdentity(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		model string
		want  bool
	}{
		{name: "Windows folded reserved name", goos: "windows", model: ".LOCKS", want: true},
		{name: "Linux exact reserved name", goos: "linux", model: ".locks", want: true},
		{name: "Linux case-distinct name", goos: "linux", model: ".LOCKS", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateModelIdentitiesForOS(GatewayConfig{Models: map[string]Model{tt.model: {}}}, tt.goos)
			if tt.want && (err == nil || !strings.Contains(err.Error(), "reserved")) {
				t.Fatalf("validation error = %v, want reserved directory rejection", err)
			}
			if !tt.want && err != nil {
				t.Fatalf("validation rejected case-distinct Linux directory: %v", err)
			}
		})
	}
}
