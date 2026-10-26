package cmddesc

// pg2-02knr: uuidgen and tr — the pure id generator and character filter the
// drain / spec skills use to mint session and actor ids
// (`echo "$(uuidgen | tr A-Z a-z)-drain"`). Without a spec the engine could not
// verify the inner commands of the `$(...)` substitution, so the whole form
// was denied (pg2-kxmpe).
//
// Operator ruling (Phillip, 2026-10-25): uuidgen and tr MUST be allowed on
// read/write-allowed paths. Neither reads or writes a path operand: uuidgen
// takes no operands and tr takes character-set operands (STRING1 [STRING2]),
// never files, and reads only standard input. A `tr ... < FILE` redirect is
// judged as the redirect's own PathRead effect, so a non-allowed source still
// abstains there.

// uuidgenSchema: `uuidgen` prints one new UUID (no operands). Only the help
// flags are modeled; the BSD `-hdr` header form and the util-linux selectors
// (-r, -t, -s, -x, -N, ...) are unmodeled → abstain (they differ per
// platform and no skill uses them). It is exempt from the --help drift hash:
// macOS ships a BSD uuidgen and linux a util-linux one with different help.
var uuidgenSchema = CommandSchema{
	Name:         "uuidgen",
	Provenance:   "uuidgen --help (macOS /usr/bin/uuidgen, usage: uuidgen [-hdr]) and man uuidgen(1), this host 2026-10-25",
	Flags:        map[string]FlagSpec{"-h": inert, "--help": inert},
	Positionals:  PositionalSpec{Rest: Unmodeled},
	Stdin:        StdinNever,
	Stdout:       StdoutMetadata,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}

// trSchema: `tr [OPTION]... STRING1 [STRING2]` translates, squeezes or
// deletes characters read from STANDARD INPUT only; its operands are character
// sets, not files, so they are literals. The modeled flags are the ones the
// GNU coreutils and BSD synopses share (-c, -C, -d, -s) plus the GNU long
// spellings and -t; the BSD-only `-u` is unmodeled → abstain.
var trSchema = CommandSchema{
	Name:       "tr",
	Provenance: "tr (GNU coreutils) 9.11, tr --help; man tr(1) (macOS)",
	Flags: map[string]FlagSpec{
		"-c": inert, "-C": inert, "--complement": inert,
		"-d": inert, "--delete": inert,
		"-s": inert, "--squeeze-repeats": inert,
		"-t": inert, "--truncate-set1": inert,
		"--help": inert, "--version": inert,
	},
	Positionals:  PositionalSpec{Rest: Literal},
	Stdin:        StdinAlways,
	Stdout:       StdoutContent,
	UnknownFlag:  UnknownFlagInsufficient,
	EndOfOptions: true,
}
