// Package entity is the identity of a pull request inside the daemon and the
// rule that decides when its mergeability has changed.
//
// # Identity
//
// On the wire a PR is addressed as `owner/repo#n` (the form
// internal/github's callers already use). Matching is case-insensitive on
// `owner/repo`; the canonical display form is GitHub's own (the
// repository's nameWithOwner as GitHub spells it). Inside the store an entity
// is keyed by its GraphQL node id, which survives a repository rename or
// transfer; the alias table maps that node id to the current `owner/repo#n`.
//
//   - Ref parses, formats and compares references.
//   - Identity reads and updates the alias table and says when an update was a
//     rename.
//   - Resolver turns references the store has never seen into node ids, in one
//     batched GraphQL document per call.
//
// # Mergeability
//
// Carry holds the rule for GitHub's lazily computed mergeable state: UNKNOWN is
// never a change, a move between two known values is one, and a head or base
// change clears the carried value.
package entity

import (
	"fmt"
	"strconv"
	"strings"
)

// KindPR is the `kind` of a pull request in the store's entity and
// entity_group tables.
const KindPR = "pr"

// Ref is a pull request reference: owner/repo#number. Owner and Repo keep the
// spelling they were parsed or learned with; use Key or Equal to compare.
type Ref struct {
	Owner  string
	Repo   string
	Number int
}

// ParseRef parses "owner/repo#n". It rejects anything else, including a
// missing or non-positive number and an owner or repository with characters
// GitHub does not allow.
func ParseRef(s string) (Ref, error) {
	nameWithOwner, num, ok := strings.Cut(s, "#")
	if !ok {
		return Ref{}, fmt.Errorf("entity: %q is not owner/repo#n: no #", s)
	}
	owner, repo, ok := strings.Cut(nameWithOwner, "/")
	if !ok {
		return Ref{}, fmt.Errorf("entity: %q is not owner/repo#n: no /", s)
	}
	if !validOwner(owner) {
		return Ref{}, fmt.Errorf("entity: %q is not owner/repo#n: bad owner %q", s, owner)
	}
	if !validRepo(repo) {
		return Ref{}, fmt.Errorf("entity: %q is not owner/repo#n: bad repository %q", s, repo)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 || strconv.Itoa(n) != num {
		return Ref{}, fmt.Errorf("entity: %q is not owner/repo#n: bad number %q", s, num)
	}
	return Ref{Owner: owner, Repo: repo, Number: n}, nil
}

// NewRef builds a Ref from GitHub's nameWithOwner ("owner/repo") and a PR
// number.
func NewRef(nameWithOwner string, number int) (Ref, error) {
	return ParseRef(nameWithOwner + "#" + strconv.Itoa(number))
}

// String is the wire and display form, owner/repo#n.
func (r Ref) String() string {
	return r.Owner + "/" + r.Repo + "#" + strconv.Itoa(r.Number)
}

// NameWithOwner is "owner/repo".
func (r Ref) NameWithOwner() string { return r.Owner + "/" + r.Repo }

// Key is the case-folded form two references are compared by.
func (r Ref) Key() string { return strings.ToLower(r.String()) }

// Equal reports whether r and o name the same PR, ignoring the case of owner
// and repository.
func (r Ref) Equal(o Ref) bool { return r.Key() == o.Key() }

// IsZero reports whether r is the zero value.
func (r Ref) IsZero() bool { return r == Ref{} }

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// validOwner accepts GitHub's login alphabet: letters, digits and hyphens, not
// starting with a hyphen.
func validOwner(s string) bool {
	if s == "" || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) && s[i] != '-' {
			return false
		}
	}
	return true
}

// validRepo accepts letters, digits, hyphen, underscore and dot, and refuses
// the two names ("." and "..") GitHub reserves.
func validRepo(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) && s[i] != '-' && s[i] != '_' && s[i] != '.' {
			return false
		}
	}
	return true
}
