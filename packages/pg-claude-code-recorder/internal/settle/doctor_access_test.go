package settle

import "sync"

// Test access to the doctor's own state, so the tests do not name where it lives.

func (s *Settler) doctorSeamRef() *doctorSeam { return &s.doctor.seam }

func (s *Settler) doctorMuRef() *sync.Mutex { return &s.doctor.mu }

// setDoctorBudget changes bytes_per_slice after the settler was built.
func (s *Settler) setDoctorBudget(n int64) { s.doctor.bytesPerSlice = n }

func (s *Settler) loadCursor() DoctorCursor { return s.doctor.loadCursor() }

func (s *Settler) saveCursor(c *DoctorCursor) error { return s.doctor.saveCursor(c) }
