package server

// commandChildren preserves existing background services while cancelling
// descendants created by the command currently using the persistent shell.
type commandChildren interface {
	Cancel(interrupt func()) error
	Close() error
}

type untrackedCommandChildren struct{}

func (untrackedCommandChildren) Cancel(interrupt func()) error { interrupt(); return nil }
func (untrackedCommandChildren) Close() error                  { return nil }
