package servetest

import (
	"encoding/json"
	"strings"
	"testing"
)

// Everything here is generic: the project key, the people and the host are
// invented placeholders (RFC 2606 example domains), never a real tenant.

// Fixture names a test can rely on.
const (
	// Project is the project key of every fixture issue.
	Project = "EXA"
	// QueryMine and QueryTeam are the configured queries of the default config;
	// MineJQL is watched, TeamJQL is not.
	QueryMine = "mine"
	QueryTeam = "team"
	MineJQL   = "project = EXA AND assignee = currentUser()"
	TeamJQL   = "project = EXA AND labels = needs-review"
	// Updated is the fixed time of an issue that names none.
	Updated = "2026-10-12T08:00:00.000+0000"
)

// Person is a fixture user.
type Person struct {
	Email string
	Name  string
}

// FixturePerson is the one person the fixtures use for the assignee, the
// reporter and the authors.
var FixturePerson = Person{Email: "fixture.person@example.test", Name: "Fixture Person"}

func (p Person) doc() map[string]any {
	return map[string]any{"email": p.Email, "account_id": "acct-" + strings.ReplaceAll(strings.ToLower(p.Name), " ", "-"), "display_name": p.Name}
}

// Comment is one fixture comment.
type Comment struct {
	ID      string
	Author  Person
	Body    string
	Created string
}

// Transition is one fixture status change of an issue's changelog.
type Transition struct {
	ID     string
	From   string
	To     string
	Author Person
	At     string
}

// Issue is one fixture Jira issue, in the shape pjira prints it.
type Issue struct {
	// Key is required (for example EXA-1); the rest have defaults.
	Key     string
	Summary string
	// Status defaults to "To Do" and StatusCategory to the category of the
	// status ("new" for To Do, "indeterminate" for In Progress, "done" for Done).
	Status         string
	StatusCategory string
	IssueType      string
	Priority       string
	Parent         string
	Labels         []string
	Assignee       *Person
	Reporter       *Person
	Updated        string
	Created        string
	Comments       []Comment
	Changelog      []Transition
}

// Open is a fixture issue still to do, assigned to FixturePerson.
func Open(key string) Issue {
	p := FixturePerson
	return Issue{Key: key, Assignee: &p, Reporter: &p}
}

// Later is the updated time of an issue changed after the fixed Updated: Jira
// moves an issue's updated time with every change, and the daemon revalidates
// an issue's detail on that token.
const Later = "2026-10-13T08:00:00.000+0000"

// WithStatus returns a copy of the issue in another status (and its category),
// updated at Later, as the origin shows an issue it changed.
func (i Issue) WithStatus(status string) Issue {
	i.Status, i.StatusCategory, i.Updated = status, "", Later
	return i
}

func categoryOf(status string) string {
	switch status {
	case "In Progress":
		return "indeterminate"
	case "Done":
		return "done"
	}
	return "new"
}

// document renders the issue as the one JSON line pjira prints.
func (i Issue) document(t testing.TB) string {
	t.Helper()
	status := i.Status
	if status == "" {
		status = "To Do"
	}
	category := i.StatusCategory
	if category == "" {
		category = categoryOf(status)
	}
	summary := i.Summary
	if summary == "" {
		summary = "Example issue " + i.Key
	}
	issueType := i.IssueType
	if issueType == "" {
		issueType = "Task"
	}
	priority := i.Priority
	if priority == "" {
		priority = "Medium"
	}
	updated := i.Updated
	if updated == "" {
		updated = Updated
	}
	created := i.Created
	if created == "" {
		created = "2026-10-01T08:00:00.000+0000"
	}
	labels := i.Labels
	if labels == nil {
		labels = []string{}
	}
	doc := map[string]any{
		"key":             i.Key,
		"summary":         summary,
		"status":          status,
		"status_category": category,
		"issuetype":       issueType,
		"labels":          labels,
		"url":             "https://jira.example.test/browse/" + i.Key,
		"priority":        priority,
		"project":         Project,
		"updated":         updated,
		"created":         created,
	}
	if i.Parent != "" {
		doc["parent"] = i.Parent
	}
	if i.Assignee != nil {
		doc["assignee"] = i.Assignee.doc()
	}
	if i.Reporter != nil {
		doc["reporter"] = i.Reporter.doc()
	}
	changelog := []map[string]any{}
	for _, c := range i.Changelog {
		changelog = append(changelog, map[string]any{"id": c.ID, "field": "status", "from": c.From, "to": c.To, "author": c.Author.doc(), "at": c.At})
	}
	comments := []map[string]any{}
	for _, c := range i.Comments {
		created := c.Created
		if created == "" {
			created = "2026-10-02T08:00:00.000+0000"
		}
		comments = append(comments, map[string]any{"id": c.ID, "author": c.Author.doc(), "body": c.Body, "created": created})
	}
	doc["changelog"], doc["comments"] = changelog, comments
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// identityDocument is the answer of the operator identity lookup: one issue the
// operator reported.
func identityDocument() string {
	p := FixturePerson
	doc := map[string]any{
		"items": []map[string]any{{
			"key": Project + "-900", "summary": "identity probe", "status": "Done", "status_category": "done",
			"issuetype": "Task", "labels": []string{}, "url": "https://jira.example.test/browse/" + Project + "-900",
			"project": Project, "reporter": p.doc(),
		}},
		"truncated": false,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}
