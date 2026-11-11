package settle

import (
	"context"
	"time"
)

// The settler's doctor operations forward to its Doctor (see doctor_rolling.go),
// so serve and the tests keep one entry point.

// SetDoctorIdleStep sets the step an idle doctor slice runs; see Doctor.SetIdleStep.
func (s *Settler) SetDoctorIdleStep(fn func() error) { s.doctor.SetIdleStep(fn) }

// Doctor runs the full comparison and returns its result.
func (s *Settler) Doctor() (DoctorResult, error) {
	a, err := s.DoctorRun()
	return a.DoctorResult, err
}

// DoctorRun runs doctor to completion; see Doctor.Run.
func (s *Settler) DoctorRun() (DoctorAnswer, error) { return s.DoctorRunCtx(s.detached()) }

// DoctorRunCtx is DoctorRun under ctx; see Doctor.Run.
func (s *Settler) DoctorRunCtx(ctx context.Context) (DoctorAnswer, error) {
	return s.doctor.Run(ctx)
}

// DoctorTick is one wake of the rolling loop; see Doctor.Tick.
func (s *Settler) DoctorTick() error { return s.doctor.Tick(s.detached()) }

// DoctorProjection returns the copy bytes and the projected cycle length; see
// Doctor.Projection.
func (s *Settler) DoctorProjection() (int64, time.Duration, error) { return s.doctor.Projection() }
