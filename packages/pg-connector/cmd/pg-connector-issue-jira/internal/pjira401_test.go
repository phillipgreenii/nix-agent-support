package internal

import "testing"

func TestIsPjira401IsNarrowerThanClassify(t *testing.T) {
	cases := map[string]bool{
		"pjira issue PROJ-1: status 401 Unauthorized": true,
		"pjira create: unauthenticated":               true,
		"pjira create: nothing to see":                false,
		"pjira issue PROJ-1: status 403 Forbidden":    false,
		"pjira issue PROJ-401: status 404 Not Found":  false,
		"pjira issue PROJ-1: status 4010 odd":         false,
	}
	for text, want := range cases {
		if got := IsPjira401(text); got != want {
			t.Errorf("IsPjira401(%q) = %v, want %v", text, got, want)
		}
	}
	if ClassifyPjiraError("pjira issue PROJ-1: status 403 Forbidden") == nil {
		t.Error("ClassifyPjiraError must keep mapping a 403 (unchanged)")
	}
}
