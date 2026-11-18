package parser

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"

// ParseStoredForTest runs the per-file preparation and parse of files already
// fetched, as parseFiles does after its own fetch loop.
func (p *Parser) ParseStoredForTest(files []store.File, stop <-chan struct{}) (int, error) {
	p.run.Lock()
	defer p.run.Unlock()
	return p.parseStored(files, nil, stop)
}
