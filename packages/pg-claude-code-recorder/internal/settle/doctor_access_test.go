package settle

import "sync"

// Test access to the doctor's own state, so the tests do not name where it lives.

func (s *Settler) doctorSeamRef() *doctorSeam { return &s.doctorSeam }

func (s *Settler) doctorMuRef() *sync.Mutex { return &s.doctorMu }

// setDoctorBudget changes bytes_per_slice after the settler was built.
func (s *Settler) setDoctorBudget(n int64) { s.cfg.DoctorBytesPerSlice = n }
