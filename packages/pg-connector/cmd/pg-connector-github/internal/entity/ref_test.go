package entity

import "testing"

func TestParseRef_RoundTripAndCase(t *testing.T) {
	for _, in := range []string{"owner/repo#1", "Some-Owner/My.Repo_x#42", "o/r#123456"} {
		ref, err := ParseRef(in)
		if err != nil {
			t.Fatalf("ParseRef(%q): %v", in, err)
		}
		if got := ref.String(); got != in {
			t.Errorf("round trip of %q gave %q", in, got)
		}
		again, err := ParseRef(ref.String())
		if err != nil || again != ref {
			t.Errorf("second parse of %q = %+v, %v", in, again, err)
		}
	}
	ref, _ := ParseRef("Some-Owner/My.Repo#7")
	if ref.Owner != "Some-Owner" || ref.Repo != "My.Repo" || ref.Number != 7 || ref.NameWithOwner() != "Some-Owner/My.Repo" {
		t.Errorf("fields = %+v", ref)
	}
}

func TestRef_EqualIgnoresCaseOfOwnerAndRepoOnly(t *testing.T) {
	a, _ := ParseRef("Some-Owner/My.Repo#7")
	b, _ := ParseRef("some-owner/my.repo#7")
	c, _ := ParseRef("some-owner/my.repo#8")
	if !a.Equal(b) || a.Key() != b.Key() {
		t.Errorf("%s and %s must match", a, b)
	}
	if a.Equal(c) {
		t.Errorf("%s and %s must not match", a, c)
	}
	if a == b {
		t.Error("the structs keep their own spelling")
	}
}

func TestParseRef_Rejects(t *testing.T) {
	for _, in := range []string{
		"", "owner/repo", "owner/repo#", "owner/repo#0", "owner/repo#-1", "owner/repo#01", "owner/repo#1x",
		"owner#1", "/repo#1", "owner/#1", "-owner/repo#1", "ow ner/repo#1", "owner/re po#1", "owner/../x#1",
		"owner/..#1", "owner/./x#1", "a/b/c#1", "owner/repo#1#2",
	} {
		if ref, err := ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) = %+v, want an error", in, ref)
		}
	}
}

func TestNewRef(t *testing.T) {
	ref, err := NewRef("Org/Repo", 5)
	if err != nil || ref.String() != "Org/Repo#5" {
		t.Errorf("NewRef = %v, %v", ref, err)
	}
	if _, err := NewRef("Org", 5); err == nil {
		t.Error("NewRef without a repository must fail")
	}
}
