package main

// Blank import: every rule file in internal/rules registers itself from
// init(), so importing the package here is what puts the complete rule set
// into the shipped binary's registry. Rule packets add files to that package
// and never edit this one.
import _ "github.com/phillipgreenii/pg-decider/internal/rules"
