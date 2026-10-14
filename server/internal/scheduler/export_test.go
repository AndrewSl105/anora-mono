package scheduler

// CutStream closes a live runner's attach connection, as a proxy timeout would.
func (s *Scheduler) CutStream(taskID int64) bool {
	r, ok := s.liveRunner(taskID)
	if ok {
		r.Close()
	}
	return ok
}
