package engine

// export_test.go exposes the unexported stage-observer hook to the external
// engine_test package so the stage-order and draw-count tests can watch the
// pipeline without the observer becoming part of the production API. It is a
// _test.go file, so it ships in no build.

// WithObserverForTest returns a copy of p that reports each stage it enters to
// fn, in order. It is the test-only accessor that installs the unexported
// stage observer directly, so the observer field stays out of the public API.
func (p *Pipeline) WithObserverForTest(fn func(StageID)) *Pipeline {
	cp := *p
	cp.observe = stageObserver(fn)
	return &cp
}
