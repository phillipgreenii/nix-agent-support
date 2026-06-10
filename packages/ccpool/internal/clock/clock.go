// Package clock is the time port: production uses Real, tests use Fake.
package clock

import "time"

type Clock interface{ Now() time.Time }

type Real struct{}

func (Real) Now() time.Time { return time.Now() }

type Fake struct{ T time.Time }

func (f *Fake) Now() time.Time          { return f.T }
func (f *Fake) Advance(d time.Duration) { f.T = f.T.Add(d) }
