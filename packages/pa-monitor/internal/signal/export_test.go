package signal

// EnrichCmdErrForTest exposes enrichCmdErr for whitebox testing.
func EnrichCmdErrForTest(err error) error { return enrichCmdErr(err) }
