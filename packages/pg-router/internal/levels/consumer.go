package levels

// Consumer is one consumer of an emitter: the requirement of a registration
// that binds an event type the emitter produces, and whether that registration
// is enabled. The queue polls an emitter when ANY enabled consumer could run.
type Consumer struct {
	Req     Requirement
	Enabled bool
}
