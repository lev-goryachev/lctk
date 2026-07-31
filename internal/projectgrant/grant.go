// Package projectgrant issues and validates the credentials that let a client
// reach a project endpoint.
//
// A grant names a client, the projects it may reach, and when it expires. Per
// docs/security.md one project's key must not open another project, so
// validation always takes the project the caller is trying to reach and answers
// for that project alone.
//
// Grants live in the per-user LCTK home, never in a repository. They are secrets
// in the same sense as the credentials the editor already keeps in the user
// profile: the first release does not claim to protect against the machine
// owner, but it does keep credentials out of Git and out of any generated file a
// repository could carry.
package projectgrant

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the on-disk schema version of the grant document.
const SchemaVersion = 1

// DefaultClient is the client name used when a grant is issued automatically.
const DefaultClient = "lctk-local"

// tokenBytes is the entropy of a generated token. 256 bits is far beyond what a
// loopback credential needs and costs nothing.
const tokenBytes = 32

// Errors a caller is expected to distinguish. The gateway maps them to typed
// wire errors, so they must stay separable.
var (
	// ErrNoGrant reports that no grant matches the presented credential.
	ErrNoGrant = errors.New("no grant matches the presented credential")
	// ErrProjectNotPermitted reports a valid credential that does not cover the
	// requested project. This is deliberately distinct from ErrNoGrant so the
	// caller learns the credential is real but scoped elsewhere.
	ErrProjectNotPermitted = errors.New("the grant does not permit this project")
	// ErrGrantExpired reports a credential past its expiry.
	ErrGrantExpired = errors.New("the grant has expired")
	// ErrGrantNotFound reports a lookup by name or project that matched nothing.
	ErrGrantNotFound = errors.New("grant not found")
)

// Grant is one issued credential.
//
// Token is stored in clear text because LCTK must be able to place it into the
// environment of an editor it configures; a hash would make the credential
// unrecoverable and force a rotation on every configuration write. The file is
// owner-only and outside any repository.
type Grant struct {
	ID         string    `json:"id"`
	Client     string    `json:"client"`
	ProjectIDs []string  `json:"project_ids"`
	Token      string    `json:"token"`
	IssuedAt   time.Time `json:"issued_at"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Revoked    bool      `json:"revoked,omitempty"`
}

// Expired reports whether the grant is past its expiry at the given instant. A
// zero expiry means the grant does not expire on its own.
func (g Grant) Expired(now time.Time) bool {
	return !g.ExpiresAt.IsZero() && now.After(g.ExpiresAt)
}

// Permits reports whether the grant covers a project.
func (g Grant) Permits(projectID string) bool {
	for _, id := range g.ProjectIDs {
		if id == projectID {
			return true
		}
	}
	return false
}

// Redacted returns a copy safe to print or serialize into human output.
func (g Grant) Redacted() Grant {
	g.Token = ""
	return g
}

// Set is an in-memory view of the issued grants.
type Set struct {
	grants []Grant
}

// New returns an empty set.
func New() *Set { return &Set{} }

// List returns the grants ordered by identifier, tokens included. Callers that
// display grants should redact them.
func (s *Set) List() []Grant {
	out := make([]Grant, len(s.grants))
	copy(out, s.grants)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len reports the number of grants.
func (s *Set) Len() int { return len(s.grants) }

// EnsureForProject returns the existing usable grant for a project and client, or
// issues one.
//
// This is the automatic grant of roadmap Slice 1.3: registering a project is
// enough to reach it locally, with no credential the user has to copy.
func (s *Set) EnsureForProject(projectID, client string, now time.Time) (Grant, error) {
	if projectID == "" {
		return Grant{}, errors.New("project id is empty")
	}
	if client == "" {
		client = DefaultClient
	}

	for _, grant := range s.grants {
		if grant.Client == client && !grant.Revoked && !grant.Expired(now) && grant.Permits(projectID) {
			return grant, nil
		}
	}
	return s.Issue(client, []string{projectID}, time.Time{}, now)
}

// Issue creates a grant with a freshly generated token.
func (s *Set) Issue(client string, projectIDs []string, expiresAt time.Time, now time.Time) (Grant, error) {
	if client == "" {
		client = DefaultClient
	}
	if len(projectIDs) == 0 {
		return Grant{}, errors.New("a grant must permit at least one project")
	}

	token, err := generateToken()
	if err != nil {
		return Grant{}, err
	}
	id, err := generateID()
	if err != nil {
		return Grant{}, err
	}

	permitted := make([]string, len(projectIDs))
	copy(permitted, projectIDs)
	sort.Strings(permitted)

	grant := Grant{
		ID:         id,
		Client:     client,
		ProjectIDs: permitted,
		Token:      token,
		IssuedAt:   now.UTC().Truncate(time.Second),
		ExpiresAt:  expiresAt.UTC().Truncate(time.Second),
	}
	if expiresAt.IsZero() {
		grant.ExpiresAt = time.Time{}
	}
	s.grants = append(s.grants, grant)
	return grant, nil
}

// Resolve validates a presented token against a project.
//
// The project comes from the route, never from the caller's payload, so this
// function is the single place where "which project may this credential reach"
// is decided.
func (s *Set) Resolve(token, projectID string, now time.Time) (Grant, error) {
	presented := strings.TrimSpace(token)
	if presented == "" {
		return Grant{}, ErrNoGrant
	}

	// Every grant is compared with a constant-time check, and the loop is not
	// short-circuited on a mismatch, so timing does not reveal which grant exists.
	var matched *Grant
	for i := range s.grants {
		if subtle.ConstantTimeCompare([]byte(s.grants[i].Token), []byte(presented)) == 1 {
			matched = &s.grants[i]
		}
	}
	if matched == nil || matched.Revoked {
		return Grant{}, ErrNoGrant
	}
	if matched.Expired(now) {
		return Grant{}, ErrGrantExpired
	}
	if !matched.Permits(projectID) {
		return Grant{}, ErrProjectNotPermitted
	}
	return *matched, nil
}

// ForProject returns the usable grant covering a project, if one exists.
func (s *Set) ForProject(projectID string, now time.Time) (Grant, error) {
	for _, grant := range s.grants {
		if !grant.Revoked && !grant.Expired(now) && grant.Permits(projectID) {
			return grant, nil
		}
	}
	return Grant{}, fmt.Errorf("%w: no usable grant covers project %s", ErrGrantNotFound, projectID)
}

// Revoke marks a grant unusable without deleting the record, so an audit of what
// was issued remains possible.
func (s *Set) Revoke(id string) (Grant, error) {
	for i := range s.grants {
		if s.grants[i].ID == id {
			s.grants[i].Revoked = true
			return s.grants[i], nil
		}
	}
	return Grant{}, fmt.Errorf("%w: %s", ErrGrantNotFound, id)
}

// RevokeForProject revokes every grant covering a project and drops the project
// from grants that also cover others, so removing one project does not disable a
// client's access to the rest.
func (s *Set) RevokeForProject(projectID string) int {
	affected := 0
	for i := range s.grants {
		if !s.grants[i].Permits(projectID) {
			continue
		}
		affected++
		if len(s.grants[i].ProjectIDs) == 1 {
			s.grants[i].Revoked = true
			continue
		}
		remaining := make([]string, 0, len(s.grants[i].ProjectIDs)-1)
		for _, id := range s.grants[i].ProjectIDs {
			if id != projectID {
				remaining = append(remaining, id)
			}
		}
		s.grants[i].ProjectIDs = remaining
	}
	return affected
}

func generateToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate grant token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func generateID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate grant id: %w", err)
	}
	return "grant-" + base64.RawURLEncoding.EncodeToString(raw), nil
}
