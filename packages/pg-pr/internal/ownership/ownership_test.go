package ownership

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		e    Engagement
		want Ownership
	}{
		{"authored-by-me => mine", Engagement{Self: "me", PRAuthor: "me"}, Mine},
		{"mine wins even with others' commits", Engagement{Self: "me", PRAuthor: "me", CommitAuthors: []string{"you"}}, Mine},
		{"teammate + my commit => co-owned", Engagement{Self: "me", PRAuthor: "you", CommitAuthors: []string{"you", "me"}}, CoOwned},
		{"teammate + no commit of mine => team", Engagement{Self: "me", PRAuthor: "you", CommitAuthors: []string{"you"}}, Team},
		{"empty self => team", Engagement{Self: "", PRAuthor: "me", CommitAuthors: []string{"me"}}, Team},
		{"nil commits (degrade) teammate => team", Engagement{Self: "me", PRAuthor: "you"}, Team},
		{"nil commits (degrade) mine => mine", Engagement{Self: "me", PRAuthor: "me"}, Mine},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.e); got != tt.want {
				t.Errorf("Classify(%+v) = %q, want %q", tt.e, got, tt.want)
			}
		})
	}
}

func TestActsAsMine(t *testing.T) {
	if !Mine.ActsAsMine() || !CoOwned.ActsAsMine() || Team.ActsAsMine() {
		t.Errorf("ActsAsMine: Mine=%v CoOwned=%v Team=%v; want true true false",
			Mine.ActsAsMine(), CoOwned.ActsAsMine(), Team.ActsAsMine())
	}
}
