package posted

// Verdict is the classification of one requested comment.
type Verdict string

const (
	// AlreadyPresent: the fingerprint's marker is on a comment the viewer
	// authored on the PR, read from GitHub (pending or submitted reviews).
	// GitHub is the machine-independent source of truth.
	AlreadyPresent Verdict = "already_present"
	// Dismissed: the sidecar confirms the fingerprint was posted but GitHub
	// holds it nowhere, so the operator deleted it. It is never posted again.
	Dismissed Verdict = "dismissed"
	// ToWrite: neither GitHub nor the sidecar knows the fingerprint.
	ToWrite Verdict = "to_write"
)

// Classify returns one Verdict per requested fingerprint, in request order. It
// is pure: onGitHub is the set of fingerprints whose markers were found on any
// comment the viewer authored on the PR (every pending review and submitted
// reviews with their threads), and sc is the loaded sidecar. GitHub wins over
// the sidecar.
func Classify(requested []string, onGitHub map[string]bool, sc State) []Verdict {
	out := make([]Verdict, len(requested))
	for i, fp := range requested {
		switch {
		case onGitHub[fp]:
			out[i] = AlreadyPresent
		case sc.Has(fp):
			out[i] = Dismissed
		default:
			out[i] = ToWrite
		}
	}
	return out
}
