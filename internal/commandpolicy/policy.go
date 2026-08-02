// Package commandpolicy decides which commands a project may run.
//
// The shape of the decision is the point. A repository *proposes* commands in
// its manifest; the machine owner *approves* them; and a client may then run one
// by name and only by name. A client never supplies a command line, so the set of
// things it can execute is exactly the set a human read and agreed to.
//
// Approval is bound to the exact text that was approved. If the manifest later
// changes `test` from `go test ./...` to something else, the approval lapses and
// has to be given again. Without that, a repository could get a command approved
// and then quietly replace it, which is the whole attack this package exists to
// prevent.
//
// The image is approved the same way and for the same reason. Choosing what
// container a command runs in is choosing what it can do, so it is the machine
// owner's decision rather than the repository's.
package commandpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Names a manifest may propose. The set is closed on purpose: a fixed vocabulary
// is what lets a client ask for "the tests" without knowing anything about how
// this project runs them, and it keeps a manifest from inventing a command
// surface nobody reviewed.
const (
	NameBuild = "build"
	NameTest  = "test"
	NameLint  = "lint"
)

// Names lists the vocabulary in a stable order.
var Names = []string{NameBuild, NameTest, NameLint}

// Errors a caller can act on.
var (
	ErrUnknownName = errors.New("not a command LCTK knows")
	ErrNotProposed = errors.New("the project's manifest does not propose this command")
	ErrNotApproved = errors.New("this command has not been approved")
	ErrChanged     = errors.New("the command changed since it was approved")
	ErrNoImage     = errors.New("no runner image has been approved for this project")
)

// Approval is what the machine owner agreed to, stored in the registry.
type Approval struct {
	Name string `json:"name"`
	// Digest fixes the exact command text that was approved. The text itself is
	// not stored: it lives in the manifest, and keeping a second copy would
	// invite the two to disagree about which one is authoritative.
	Digest     string    `json:"digest"`
	ApprovedAt time.Time `json:"approved_at"`
}

// Set is a project's approvals.
type Set struct {
	// Image is the container the approved commands run in. A project without one
	// can run nothing, which is the safe default: LCTK cannot guess a project's
	// toolchain, and guessing wrong would run a build in an environment that
	// silently differs from the developer's.
	Image string `json:"image,omitempty"`
	// Network is "none" or "full" for this project's commands. Empty means none.
	Network   string     `json:"network,omitempty"`
	Approvals []Approval `json:"approvals,omitempty"`
}

// Proposal is one command a manifest offers.
type Proposal struct {
	Name    string
	Command string
}

// Resolved is a command that may actually be run.
type Resolved struct {
	Name    string
	Command string
	Image   string
	Network string
}

// Digest fixes a command's text.
//
// The text is normalized only by trimming surrounding whitespace. Nothing else:
// collapsing inner spacing or lowercasing would make two genuinely different
// command lines hash the same, and this value is the whole basis for saying "the
// thing I approved is the thing that will run".
func Digest(command string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// Valid reports whether a name is in the vocabulary.
func Valid(name string) bool {
	switch name {
	case NameBuild, NameTest, NameLint:
		return true
	}
	return false
}

// ValidNetwork reports whether a network policy is one of the two allowed.
func ValidNetwork(policy string) bool {
	switch policy {
	case "", "none", "full":
		return true
	}
	return false
}

// NetworkOrDefault resolves the policy, defaulting to no network.
//
// The default is no network because a build that does not need the internet
// should not have it, and a project that does need it says so once. The reverse
// default would give every command egress by accident.
func (s Set) NetworkOrDefault() string {
	if s.Network == "full" {
		return "full"
	}
	return "none"
}

// Approve records the owner's agreement to a proposed command.
func (s *Set) Approve(name, command string, now time.Time) error {
	if !Valid(name) {
		return fmt.Errorf("%w: %q", ErrUnknownName, name)
	}
	if strings.TrimSpace(command) == "" {
		return fmt.Errorf("%w: %q", ErrNotProposed, name)
	}
	digest := Digest(command)
	for i := range s.Approvals {
		if s.Approvals[i].Name == name {
			s.Approvals[i].Digest = digest
			s.Approvals[i].ApprovedAt = now.UTC()
			return nil
		}
	}
	s.Approvals = append(s.Approvals, Approval{Name: name, Digest: digest, ApprovedAt: now.UTC()})
	sort.Slice(s.Approvals, func(a, b int) bool { return s.Approvals[a].Name < s.Approvals[b].Name })
	return nil
}

// Revoke withdraws an approval.
func (s *Set) Revoke(name string) bool {
	for i := range s.Approvals {
		if s.Approvals[i].Name == name {
			s.Approvals = append(s.Approvals[:i], s.Approvals[i+1:]...)
			return true
		}
	}
	return false
}

// Resolve decides whether a named command may run right now.
//
// Every refusal is distinct, because they call for different things from whoever
// reads them: propose it, approve it, approve it again, or name an image.
func (s Set) Resolve(name string, proposals []Proposal) (Resolved, error) {
	if !Valid(name) {
		return Resolved{}, fmt.Errorf("%w: %q", ErrUnknownName, name)
	}

	command := ""
	for _, proposal := range proposals {
		if proposal.Name == name {
			command = strings.TrimSpace(proposal.Command)
		}
	}
	if command == "" {
		return Resolved{}, fmt.Errorf("%w: %q", ErrNotProposed, name)
	}

	var approval *Approval
	for i := range s.Approvals {
		if s.Approvals[i].Name == name {
			approval = &s.Approvals[i]
		}
	}
	if approval == nil {
		return Resolved{}, fmt.Errorf("%w: %q", ErrNotApproved, name)
	}
	if approval.Digest != Digest(command) {
		// The manifest moved after the owner agreed. Refusing is the entire
		// point: a repository that could edit an approved command would have a
		// way to run anything it liked.
		return Resolved{}, fmt.Errorf("%w: %q", ErrChanged, name)
	}
	if strings.TrimSpace(s.Image) == "" {
		return Resolved{}, ErrNoImage
	}

	return Resolved{
		Name:    name,
		Command: command,
		Image:   strings.TrimSpace(s.Image),
		Network: s.NetworkOrDefault(),
	}, nil
}

// Status is what a person needs to see to decide.
type Status struct {
	Name string `json:"name"`
	// Command is the text the manifest proposes, empty when it proposes none.
	Command string `json:"command,omitempty"`
	// Proposed, Approved, and Changed are the three facts that decide whether the
	// command can run, kept separate so a status line can say which one is
	// missing rather than only that something is.
	Proposed   bool      `json:"proposed"`
	Approved   bool      `json:"approved"`
	Changed    bool      `json:"changed,omitempty"`
	Runnable   bool      `json:"runnable"`
	ApprovedAt time.Time `json:"approved_at,omitzero"`
}

// Describe reports every name in the vocabulary against the current manifest.
func (s Set) Describe(proposals []Proposal) []Status {
	byName := map[string]string{}
	for _, proposal := range proposals {
		byName[proposal.Name] = strings.TrimSpace(proposal.Command)
	}

	out := make([]Status, 0, len(Names))
	for _, name := range Names {
		command := byName[name]
		status := Status{Name: name, Command: command, Proposed: command != ""}
		for _, approval := range s.Approvals {
			if approval.Name != name {
				continue
			}
			status.Approved = true
			status.ApprovedAt = approval.ApprovedAt
			status.Changed = status.Proposed && approval.Digest != Digest(command)
		}
		status.Runnable = status.Proposed && status.Approved && !status.Changed &&
			strings.TrimSpace(s.Image) != ""
		out = append(out, status)
	}
	return out
}
