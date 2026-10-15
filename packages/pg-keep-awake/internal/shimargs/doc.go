// Package shimargs parses the caffeinate-compatible command line accepted by
// pg-caffeinate-shim:
//
//	caffeinate [-disu] [-t timeout] [-w pid] [utility arguments...]
//
// The parser is pure. It starts nothing, writes no files and never reads
// argv[0]; the only outside fact it needs, whether a watched pid exists, is
// injected. Runtime behavior (timers, signals, lease files) belongs to the
// shim program.
package shimargs
