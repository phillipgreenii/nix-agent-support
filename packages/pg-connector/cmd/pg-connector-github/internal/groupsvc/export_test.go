package groupsvc

import "context"

// Members exposes the keep-alive member set to the external test package.
func (s *Service) Members(ctx context.Context) (map[string]bool, error) { return s.members(ctx) }
