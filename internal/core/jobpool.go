package core

import "sync"

// JobPool runs blob materialize / upload jobs with bounded concurrency.
type JobPool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

// NewJobPool builds a pool clamped to 1..16 workers.
func NewJobPool(concurrency int) *JobPool {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	return &JobPool{sem: make(chan struct{}, concurrency)}
}

// Enqueue schedules a job; panics inside a job are swallowed so one bad
// attachment cannot take down the test process.
func (p *JobPool) Enqueue(job func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.sem <- struct{}{}
		defer func() { <-p.sem }()
		defer func() { _ = recover() }()
		job()
	}()
}

// Drain waits until every scheduled job finished.
func (p *JobPool) Drain() { p.wg.Wait() }
