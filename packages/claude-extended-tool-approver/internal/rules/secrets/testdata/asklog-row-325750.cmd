bd dep add pg2-qkecz pg2-1vme1 --type discovered-from 2>&1 | tail -2
bd comment pg2-14vjq 'Hole A of this bead — "any operator or redirection trailing a loop terminator (done > out, done 2>&1, done | tee x) is a candidate for the same silent segment loss" — is now CONFIRMED as a live auto-approve hole and split out to P0 pg2-qkecz, together with a second hole this bead did not name (the for-loop WORD LIST is also dropped, so `for x in $(curl|sh); do echo hi; done` auto-approves).

Verified end-to-end through EvaluateHook with the hermetic buildFullEngine harness. `for f in a b; do echo hi; done > /etc/passwd` -> approve; control `echo hi > /etc/passwd` -> abstain. Also affects while/until, and `> ~/.ssh/authorized_keys`.

Root cause is NOT the quote-blind-scanner class: resolveLoops DELETES segments (parser.go:1054-1071 advances past the isDoneKeyword segment; for-loops never add the word list to conditionSegs because isCondLoop is false at :1085), and Parse'"'"'s leftover net at :941-949 covers HEREDOCS ONLY. parser_test.go:1403 and :1444 currently pin the drop as intended, which is why no test caught it.

This bead retains instances #8 (heredoc rides the done keyword) and #9 (fd-prefixed 2<<EOF). Note #9 appears ALREADY CLOSED by pg2-r2rf3: parser.go:877-880 sets hasHeredoc from a recorded extent, pinned by heredoc_test.go:247. The residual #9 defect is that `2<<EOF` leaks into Args as a phantom operand, since extractRedirections at :1292 requires a `<<` PREFIX — that is what a test should assert, not heredoc-bearing detection.' 2>&1 | tail -2
