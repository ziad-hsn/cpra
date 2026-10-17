package httpserver

import "time"

func (s *Server) observationTimeout() time.Duration {
	if s.cfg.ReadTimeout > 0 {
		return s.cfg.ReadTimeout
	}
	return 10 * time.Second
}
