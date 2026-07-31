package projectgrant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lev-goryachev/lctk/internal/lctkhome"
)

var now = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(lctkhome.EnvOverride, home)
	return home
}

func TestIssueProducesAUsableUniqueGrant(t *testing.T) {
	set := New()

	first, err := set.Issue("client-a", []string{"alpha"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := set.Issue("client-b", []string{"beta"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}

	if first.Token == second.Token {
		t.Fatal("two grants share a token")
	}
	if first.ID == second.ID {
		t.Error("two grants share an id")
	}
	// A loopback credential still has to be unguessable.
	if len(first.Token) < 40 {
		t.Errorf("token looks too short to be unguessable: %d characters", len(first.Token))
	}
	if !first.ExpiresAt.IsZero() {
		t.Errorf("expires_at = %v, want zero for a non-expiring grant", first.ExpiresAt)
	}
	if first.Expired(now.Add(100 * 365 * 24 * time.Hour)) {
		t.Error("a zero expiry must never expire")
	}
}

func TestIssueRejectsAGrantThatPermitsNothing(t *testing.T) {
	if _, err := New().Issue("client", nil, time.Time{}, now); err == nil {
		t.Error("a grant permitting no projects was accepted")
	}
}

func TestResolveRequiresTheRightProject(t *testing.T) {
	set := New()
	alpha, err := set.Issue("client", []string{"alpha"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := set.Resolve(alpha.Token, "alpha", now); err != nil {
		t.Errorf("the grant does not work on its own project: %v", err)
	}
	// The property the whole isolation model rests on.
	if _, err := set.Resolve(alpha.Token, "beta", now); !errors.Is(err, ErrProjectNotPermitted) {
		t.Errorf("got %v, want ErrProjectNotPermitted", err)
	}
	if _, err := set.Resolve("wrong-token", "alpha", now); !errors.Is(err, ErrNoGrant) {
		t.Errorf("got %v, want ErrNoGrant", err)
	}
	if _, err := set.Resolve("", "alpha", now); !errors.Is(err, ErrNoGrant) {
		t.Errorf("empty token: got %v, want ErrNoGrant", err)
	}
	// A wrong credential and a wrongly scoped credential must stay separable, so
	// the caller can tell "bad token" from "right token, wrong project".
	if errors.Is(ErrProjectNotPermitted, ErrNoGrant) {
		t.Error("the two refusal reasons are not distinguishable")
	}
}

func TestResolveRejectsExpiredAndRevoked(t *testing.T) {
	set := New()

	expired, err := set.Issue("client", []string{"alpha"}, now.Add(-time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Resolve(expired.Token, "alpha", now); !errors.Is(err, ErrGrantExpired) {
		t.Errorf("got %v, want ErrGrantExpired", err)
	}

	live, err := set.Issue("client", []string{"alpha"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Revoke(live.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Resolve(live.Token, "alpha", now); !errors.Is(err, ErrNoGrant) {
		t.Errorf("revoked grant: got %v, want ErrNoGrant", err)
	}
	if _, err := set.Revoke("grant-missing"); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("got %v, want ErrGrantNotFound", err)
	}
}

func TestEnsureForProjectIsIdempotent(t *testing.T) {
	set := New()

	first, err := set.EnsureForProject("alpha", DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := set.EnsureForProject("alpha", DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Error("a second call issued a duplicate grant")
	}
	if set.Len() != 1 {
		t.Errorf("len = %d, want 1", set.Len())
	}

	// A revoked grant must be replaced rather than reused.
	if _, err := set.Revoke(first.ID); err != nil {
		t.Fatal(err)
	}
	third, err := set.EnsureForProject("alpha", DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Error("a revoked grant was handed back")
	}
	if _, err := set.Resolve(third.Token, "alpha", now); err != nil {
		t.Errorf("the replacement grant does not work: %v", err)
	}

	if _, err := set.EnsureForProject("", DefaultClient, now); err == nil {
		t.Error("an empty project id was accepted")
	}
}

func TestRevokeForProjectKeepsOtherProjectsWorking(t *testing.T) {
	set := New()
	shared, err := set.Issue("client", []string{"alpha", "beta"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}
	single, err := set.Issue("client-b", []string{"alpha"}, time.Time{}, now)
	if err != nil {
		t.Fatal(err)
	}

	if affected := set.RevokeForProject("alpha"); affected != 2 {
		t.Errorf("affected = %d, want 2", affected)
	}

	// The shared grant loses alpha but keeps beta.
	if _, err := set.Resolve(shared.Token, "alpha", now); !errors.Is(err, ErrProjectNotPermitted) {
		t.Errorf("alpha is still permitted: %v", err)
	}
	if _, err := set.Resolve(shared.Token, "beta", now); err != nil {
		t.Errorf("removing alpha broke beta: %v", err)
	}
	// The single-project grant is revoked outright.
	if _, err := set.Resolve(single.Token, "alpha", now); !errors.Is(err, ErrNoGrant) {
		t.Errorf("single-project grant still works: %v", err)
	}
}

func TestForProjectFindsAUsableGrant(t *testing.T) {
	set := New()
	if _, err := set.ForProject("alpha", now); !errors.Is(err, ErrGrantNotFound) {
		t.Errorf("got %v, want ErrGrantNotFound", err)
	}
	issued, err := set.EnsureForProject("alpha", DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	found, err := set.ForProject("alpha", now)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != issued.ID {
		t.Errorf("found %q, want %q", found.ID, issued.ID)
	}
}

func TestRedactedDropsTheToken(t *testing.T) {
	grant := Grant{ID: "grant-1", Token: "super-secret"}
	if redacted := grant.Redacted(); redacted.Token != "" {
		t.Errorf("token survived redaction: %q", redacted.Token)
	}
	if grant.Token != "super-secret" {
		t.Error("Redacted mutated the original")
	}
}

func TestSaveAndLoadRoundTripWithRestrictedPermissions(t *testing.T) {
	home := isolate(t)

	set := New()
	grant, err := set.EnsureForProject("alpha", DefaultClient, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Save(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, FileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Mode is advisory on Windows, so this is asserted only where it is enforced.
	if perm := info.Mode().Perm(); perm&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("grant file is group or world accessible: %v", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.Resolve(grant.Token, "alpha", now); err != nil {
		t.Errorf("the reloaded grant does not work: %v", err)
	}

	// The file must not be left with temporary artifacts.
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", entry.Name())
		}
	}

	var doc document
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", doc.SchemaVersion, SchemaVersion)
	}
}

func TestLoadWithoutAFileIsEmpty(t *testing.T) {
	isolate(t)
	set, err := Load()
	if err != nil {
		t.Fatalf("first run should not fail: %v", err)
	}
	if set.Len() != 0 {
		t.Errorf("len = %d, want 0", set.Len())
	}
}

func TestLoadRefusesCorruptAndNewerDocuments(t *testing.T) {
	home := isolate(t)
	path := filepath.Join(home, FileName)

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("a corrupt grant file was silently accepted")
	}

	if err := os.WriteFile(path, []byte(`{"schema_version":9999,"grants":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("got %v, want ErrSchemaTooNew", err)
	}
}

// TestLoadRejectsTwoGrantsSharingAToken guards the property scope enforcement
// depends on: one token must resolve to exactly one permitted set.
func TestLoadRejectsTwoGrantsSharingAToken(t *testing.T) {
	home := isolate(t)
	raw := `{"schema_version":1,"grants":[
		{"id":"grant-a","client":"c","project_ids":["alpha"],"token":"same"},
		{"id":"grant-b","client":"c","project_ids":["beta"],"token":"same"}]}`
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("got %v, want an ambiguity error", err)
	}
}

func TestLoadRejectsMalformedRecords(t *testing.T) {
	cases := map[string]string{
		"empty id":     `{"grants":[{"id":"","client":"c","project_ids":["a"],"token":"t"}]}`,
		"empty token":  `{"grants":[{"id":"g","client":"c","project_ids":["a"],"token":""}]}`,
		"no projects":  `{"grants":[{"id":"g","client":"c","project_ids":[],"token":"t"}]}`,
		"duplicate id": `{"grants":[{"id":"g","client":"c","project_ids":["a"],"token":"t1"},{"id":"g","client":"c","project_ids":["b"],"token":"t2"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			home := isolate(t)
			if err := os.WriteFile(filepath.Join(home, FileName), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(); err == nil {
				t.Error("a malformed grant document was accepted")
			}
		})
	}
}
