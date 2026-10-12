package cmddesc

import (
	"strings"
	"testing"
)

func gitPushSchemaUnderTest(t *testing.T) CommandSchema {
	t.Helper()
	git, ok := DefaultRegistry().Lookup("git")
	if !ok {
		t.Fatal("no git schema")
	}
	push, ok := git.Subcommands["push"]
	if !ok {
		t.Fatal("no git push schema")
	}
	return push
}

func remoteEffects(in Interpretation) []Effect {
	var out []Effect
	for _, e := range in.Effects {
		if e.Kind == EffectRemote {
			out = append(out, e)
		}
	}
	return out
}

// TestGitPushSchema pins the R6 push model (packet tc-o14i5.3.10): the
// formerly inert/plain-literal flags are modeled effects, force and lease are
// distinct transform kinds, refspec/delete operands are recorded on the
// effect, and --receive-pack/--exec are exec-program effects. Verdicts are
// engine policy (effectpolicy.TestGolden / TestRemoteMutationPolicy); this
// pins the FACTS the policy keys on. Flags verified against git 2.54.0
// (`git help push`, `git push -h`).
func TestGitPushSchema(t *testing.T) {
	push := gitPushSchemaUnderTest(t)

	t.Run("force and lease are distinct transform kinds", func(t *testing.T) {
		force, lease := push.Flags["--force"].Transform.Kind, push.Flags["--force-with-lease"].Transform.Kind
		if force != TransformForce || lease != TransformForceWithLease || force == lease {
			t.Errorf("--force=%v --force-with-lease=%v, want distinct TransformForce/TransformForceWithLease", force, lease)
		}
		if push.Flags["-f"].Transform.Kind != TransformForce {
			t.Errorf("-f is not TransformForce")
		}
	})

	t.Run("no push flag is left inert that changes what is pushed", func(t *testing.T) {
		for _, f := range []string{"--prune", "--all", "--tags", "--branches", "--mirror", "--delete", "-d", "--receive-pack", "--exec"} {
			spec, ok := push.Flags[f]
			if !ok {
				t.Errorf("%s is not modeled", f)
				continue
			}
			if spec.Transform.Kind == TransformNone && spec.Operand.Kind != KindExecRemote {
				t.Errorf("%s is inert", f)
			}
		}
	})

	ops := map[string]string{
		"git push --prune origin feature":                 "push-bulk",
		"git push --mirror origin":                        "push-bulk",
		"git push --all origin":                           "push-multiref",
		"git push --tags origin":                          "push-multiref",
		"git push --branches origin":                      "push-multiref",
		"git push --force origin feature":                 "force-push",
		"git push -f origin feature":                      "force-push",
		"git push --force-with-lease origin feature":      "push-lease",
		"git push --delete origin feature":                "delete-ref",
		"git push origin :feature":                        "delete-ref",
		"git push --delete --force origin feature":        "force-push",
		"git push --all --force origin":                   "force-push",
		"git push --force --all origin":                   "force-push",
		"git push --tags --prune origin":                  "push-bulk",
		"git push --prune --tags origin":                  "push-bulk",
		"git push --all --delete origin feature":          "delete-ref",
		"git push origin main":                            "push",
		"git push --all --force-with-lease origin":        "push-multiref",
		"git push --force-with-lease --all origin":        "push-multiref",
		"git push --force-with-lease --delete origin foo": "delete-ref",
	}
	for command, want := range ops {
		in := interpretDefault(t, command)
		if !in.Sufficient {
			t.Errorf("%q: insufficient: %s", command, in.Insufficiency)
			continue
		}
		re := remoteEffects(in)
		if len(re) != 1 || re[0].Operation != want {
			t.Errorf("%q: remote effects = %v, want one %q", command, re, want)
		}
	}

	t.Run("delete refs are recorded", func(t *testing.T) {
		for command, want := range map[string]string{
			"git push --delete origin feature":        "feature",
			"git push -d origin a b":                  "a\nb",
			"git push origin :feature":                "feature",
			"git push origin --delete tag v1":         "refs/tags/v1",
			"git push --delete origin refs/tags/v1":   "refs/tags/v1",
			"git push origin :feature main":           "feature",
			"git push --delete origin refs/heads/foo": "refs/heads/foo",
		} {
			re := remoteEffects(interpretDefault(t, command))
			if len(re) != 1 || re[0].DelRefs != want {
				t.Errorf("%q: DelRefs = %q, want %q (effects %v)", command, strings.Join(strings.Fields(re[0].DelRefs), ","), want, re)
			}
		}
		// A plain push records its ref and no deletion.
		re := remoteEffects(interpretDefault(t, "git push origin main"))
		if re[0].Refs != "main" || re[0].DelRefs != "" {
			t.Errorf("plain push Refs=%q DelRefs=%q", re[0].Refs, re[0].DelRefs)
		}
	})

	t.Run("--receive-pack and --exec are exec-program effects", func(t *testing.T) {
		for _, command := range []string{
			"git push --receive-pack=evil origin main",
			"git push --receive-pack evil origin main",
			"git push --exec=evil origin main",
			"git push --exec evil origin main",
			`git push --exec="$P" origin main`,
		} {
			in := interpretDefault(t, command)
			if !in.Sufficient {
				t.Errorf("%q: insufficient: %s", command, in.Insufficiency)
				continue
			}
			var found bool
			for _, e := range in.Effects {
				if e.Kind == EffectExecProgram && e.Detail == RemoteProgramDetail {
					found = true
				}
			}
			if !found {
				t.Errorf("%q: no exec-program effect with Detail %q: %v", command, RemoteProgramDetail, in.Effects)
			}
		}
	})

	t.Run("force, glob and matching-branches refspecs stay insufficient", func(t *testing.T) {
		for _, command := range []string{
			"git push origin +main",
			"git push origin refs/heads/*:refs/heads/*",
			"git push origin :",
			`git push origin "$REF"`,
		} {
			if in := interpretDefault(t, command); in.Sufficient {
				t.Errorf("%q: sufficient, want insufficient", command)
			}
		}
	})
}
