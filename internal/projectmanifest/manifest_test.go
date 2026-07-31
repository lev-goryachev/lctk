package projectmanifest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWithNoManifestIsNotAnError(t *testing.T) {
	result, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.TrackedPresent || result.LocalPresent {
		t.Error("presence was reported for files that do not exist")
	}
	if !reflect.DeepEqual(result.Manifest, Manifest{}) {
		t.Errorf("expected the zero manifest, got %+v", result.Manifest)
	}
}

func TestLoadReadsTheSafeSubset(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, `
schema_version: 1
profile: full
network: none
excludes:
  - dist/**
  - "coverage/**"
languages:
  - TypeScript
  - Go
index:
  max_file_size_kb: 512
  include_generated: false
commands:
  build: npm run build
  test: npm test
  lint: npm run lint
`)

	result, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TrackedPresent {
		t.Error("tracked manifest was not reported as present")
	}
	m := result.Manifest
	if m.Profile != "full" {
		t.Errorf("profile = %q", m.Profile)
	}
	if m.Network != "none" {
		t.Errorf("network = %q", m.Network)
	}
	if !reflect.DeepEqual(m.Excludes, []string{"dist/**", "coverage/**"}) {
		t.Errorf("excludes = %v", m.Excludes)
	}
	// Languages are normalized so that "TypeScript" and "typescript" agree.
	if !reflect.DeepEqual(m.Languages, []string{"typescript", "go"}) {
		t.Errorf("languages = %v", m.Languages)
	}
	if m.Index.MaxFileSizeKB != 512 {
		t.Errorf("max_file_size_kb = %d", m.Index.MaxFileSizeKB)
	}
	if m.Commands.Build != "npm run build" || m.Commands.Test != "npm test" || m.Commands.Lint != "npm run lint" {
		t.Errorf("commands = %+v", m.Commands)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", result.Warnings)
	}
}

// TestManifestCannotDeclareAHostPath is the roadmap's required check that the
// manifest cannot replace the authoritative host path.
func TestManifestCannotDeclareAHostPath(t *testing.T) {
	for _, field := range []string{"path", "host_path", "root", "repository_root", "workspace"} {
		dir := t.TempDir()
		writeManifest(t, dir, FileName, field+": C:\\somewhere\\else\n")

		_, err := Load(dir)
		if !errors.Is(err, ErrForbiddenField) {
			t.Errorf("%s: got %v, want ErrForbiddenField", field, err)
			continue
		}
		if !strings.Contains(err.Error(), "registry") {
			t.Errorf("%s: error should explain that the registry is authoritative: %v", field, err)
		}
	}
}

// TestManifestHasNoFieldCapableOfHoldingAPath is the structural half of the same
// guarantee: even if screening were bypassed, there is nothing to read.
func TestManifestHasNoFieldCapableOfHoldingAPath(t *testing.T) {
	forbidden := map[string]bool{
		"path": true, "hostpath": true, "root": true, "repositoryroot": true,
		"workspace": true, "mount": true, "mounts": true, "volume": true,
		"volumes": true, "grant": true, "grants": true, "secret": true,
		"secrets": true, "token": true, "tokens": true, "credentials": true,
		"capability": true, "capabilities": true, "admin": true, "projectid": true,
	}
	manifestType := reflect.TypeOf(Manifest{})
	for i := 0; i < manifestType.NumField(); i++ {
		name := strings.ToLower(manifestType.Field(i).Name)
		if forbidden[name] {
			t.Errorf("Manifest declares the field %q, which must not exist", manifestType.Field(i).Name)
		}
	}
}

func TestManifestCannotEscalatePrivileges(t *testing.T) {
	cases := map[string]string{
		"mounts":       "mounts:\n  - /:/host\n",
		"grants":       "grants:\n  - client: anything\n",
		"secrets":      "secrets:\n  api_key: value\n",
		"capabilities": "capabilities:\n  - admin\n",
		"admin":        "admin: true\n",
		"docker":       "docker:\n  socket: /var/run/docker.sock\n",
		"privileged":   "privileged: true\n",
		"project_id":   "project_id: someone-elses-project\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeManifest(t, dir, FileName, body)
		if _, err := Load(dir); !errors.Is(err, ErrForbiddenField) {
			t.Errorf("%s: got %v, want ErrForbiddenField", name, err)
		}
	}
}

func TestForbiddenFieldsAreRejectedWhenNested(t *testing.T) {
	dir := t.TempDir()
	// A forbidden declaration hidden under an allowed key must still be rejected.
	writeManifest(t, dir, FileName, "index:\n  mounts:\n    - /:/host\n")
	if _, err := Load(dir); !errors.Is(err, ErrForbiddenField) {
		t.Errorf("got %v, want ErrForbiddenField", err)
	}
}

func TestForbiddenFieldsAreRejectedRegardlessOfCase(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, "HOST_PATH: /elsewhere\n")
	if _, err := Load(dir); !errors.Is(err, ErrForbiddenField) {
		t.Errorf("got %v, want ErrForbiddenField", err)
	}
}

func TestUnknownFieldsWarnRatherThanFail(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, "profile: full\nfuture_feature: yes\n")

	result, err := Load(dir)
	if err != nil {
		t.Fatalf("an unknown field must not fail registration: %v", err)
	}
	if result.Manifest.Profile != "full" {
		t.Errorf("known fields were lost: %+v", result.Manifest)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "future_feature") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestLocalOverrideWinsFieldByField(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, `
profile: minimal
network: none
excludes:
  - dist/**
commands:
  build: npm run build
  test: npm test
`)
	writeManifest(t, dir, LocalFileName, `
profile: full
commands:
  test: npm run test:local
`)

	result, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TrackedPresent || !result.LocalPresent {
		t.Errorf("presence flags = tracked:%t local:%t", result.TrackedPresent, result.LocalPresent)
	}
	m := result.Manifest
	if m.Profile != "full" {
		t.Errorf("override did not win: profile = %q", m.Profile)
	}
	if m.Commands.Test != "npm run test:local" {
		t.Errorf("override did not win: test = %q", m.Commands.Test)
	}
	// Fields the override left alone must survive.
	if m.Commands.Build != "npm run build" {
		t.Errorf("tracked value was lost: build = %q", m.Commands.Build)
	}
	if m.Network != "none" {
		t.Errorf("tracked value was lost: network = %q", m.Network)
	}
	if !reflect.DeepEqual(m.Excludes, []string{"dist/**"}) {
		t.Errorf("tracked value was lost: excludes = %v", m.Excludes)
	}
}

func TestLocalOverrideAloneIsUsable(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, LocalFileName, "profile: full\n")

	result, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if result.TrackedPresent {
		t.Error("tracked manifest reported present")
	}
	if !result.LocalPresent || result.Manifest.Profile != "full" {
		t.Errorf("local override was not applied: %+v", result)
	}
}

func TestLocalOverrideCannotEscalateEither(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, "profile: minimal\n")
	writeManifest(t, dir, LocalFileName, "host_path: /elsewhere\n")
	if _, err := Load(dir); !errors.Is(err, ErrForbiddenField) {
		t.Errorf("got %v, want ErrForbiddenField", err)
	}
}

func TestExcludesMustStayInsideTheProject(t *testing.T) {
	cases := []string{
		"excludes:\n  - /etc/passwd\n",
		"excludes:\n  - ../outside/**\n",
		"excludes:\n  - dist/../../escape\n",
		"excludes:\n  - \"dist\\\\windows\"\n",
		"excludes:\n  - \"  \"\n",
	}
	for _, body := range cases {
		dir := t.TempDir()
		writeManifest(t, dir, FileName, body)
		if _, err := Load(dir); err == nil {
			t.Errorf("accepted an unsafe exclude pattern:\n%s", body)
		}
	}
}

func TestValidationRejectsUnknownEnumerations(t *testing.T) {
	cases := map[string]string{
		"profile": "profile: godmode\n",
		"network": "network: partial\n",
		"index":   "index:\n  max_file_size_kb: -1\n",
		"schema":  "schema_version: 9999\n",
		"empty":   "languages:\n  - \"\"\n",
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeManifest(t, dir, FileName, body)
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: accepted an invalid declaration", name)
		}
	}
}

func TestInvalidYAMLIsReported(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, "profile: [unclosed\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("invalid YAML was accepted")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Errorf("error does not name the offending file: %v", err)
	}
}

func TestEmptyManifestIsAccepted(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, FileName, "\n#  only a comment\n")

	result, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TrackedPresent {
		t.Error("an empty manifest should still be reported as present")
	}
	if result.Manifest.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want the current default", result.Manifest.SchemaVersion)
	}
}

func TestOversizedManifestIsRejected(t *testing.T) {
	dir := t.TempDir()
	body := "profile: minimal\n# " + strings.Repeat("padding ", maxSize/8+16)
	writeManifest(t, dir, FileName, body)
	if _, err := Load(dir); err == nil {
		t.Error("an oversized manifest was accepted")
	}
}

func TestParseReturnsWarningsWithoutAFile(t *testing.T) {
	manifest, warnings, err := Parse([]byte("profile: full\nmystery: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Profile != "full" {
		t.Errorf("profile = %q", manifest.Profile)
	}
	if len(warnings) != 1 {
		t.Errorf("warnings = %v", warnings)
	}
}
