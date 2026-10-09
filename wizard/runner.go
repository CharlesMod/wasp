package wizard

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Event kinds.
const (
	EvStage    = "stage"
	EvSay      = "say"
	EvProgress = "progress"
	EvEnd      = "end" // Text is the final state: done, failed, canceled
)

// Event is one thing that happened in a run.
type Event struct {
	Seq   int    `json:"seq"`
	Kind  string `json:"kind"`
	Stage int    `json:"stage"`
	Text  string `json:"text,omitempty"`
	Done  int64  `json:"done,omitempty"`
	Total int64  `json:"total,omitempty"`
}

// Run states.
const (
	StateIdle     = "idle"
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

// Snapshot is what a page reads of a run.
type Snapshot struct {
	Seq    int      `json:"seq"`
	State  string   `json:"state"`
	Error  string   `json:"error,omitempty"`
	Failed string   `json:"failed_stage,omitempty"`
	Stages []string `json:"stages"`
	Events []Event  `json:"events"`
}

// runner runs stages in one goroutine and keeps their events. Pages wait on
// a condition variable that every new event broadcasts; the only timer is
// the keepalive that ends a quiet wait.
type runner struct {
	mu       sync.Mutex
	cond     *sync.Cond
	seq      int
	events   []Event
	progress *Event // the latest progress, replaced in place
	state    string
	err      string
	names    []string
	failedAt int
	cancel   context.CancelFunc
	finished chan struct{} // closed when the current goroutine is done
}

func newRunner() *runner {
	r := &runner{state: StateIdle, failedAt: -1}
	r.cond = sync.NewCond(&r.mu)
	return r
}

func (r *runner) add(e Event) {
	r.seq++
	e.Seq = r.seq
	if e.Kind == EvProgress {
		c := e
		r.progress = &c
	} else {
		r.events = append(r.events, e)
	}
	r.cond.Broadcast()
}

// Start runs stages[from:]; it refuses when a run is under way.
func (r *runner) Start(parent context.Context, stages []Stage, from int) error {
	r.mu.Lock()
	if r.state == StateRunning {
		r.mu.Unlock()
		return errors.New("a run is already under way")
	}
	if from < 0 || from > len(stages) {
		from = 0
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.state, r.err, r.failedAt = StateRunning, "", -1
	r.progress = nil
	r.names = r.names[:0]
	for _, s := range stages {
		r.names = append(r.names, s.Name)
	}
	fin := make(chan struct{})
	r.finished = fin
	r.cond.Broadcast()
	r.mu.Unlock()

	go func() {
		defer close(fin)
		defer cancel()
		failed, err := -1, error(nil)
		for i := from; i < len(stages); i++ {
			idx := i
			r.mu.Lock()
			r.add(Event{Kind: EvStage, Stage: idx, Text: stages[idx].Name})
			r.mu.Unlock()
			var lastPct int64 = -2
			st := &Step{
				say: func(line string) {
					r.mu.Lock()
					r.add(Event{Kind: EvSay, Stage: idx, Text: line})
					r.mu.Unlock()
				},
				prog: func(done, total int64) {
					pct := int64(-1)
					if total > 0 {
						pct = done * 1000 / total
					}
					r.mu.Lock()
					if pct != lastPct || pct == -1 {
						lastPct = pct
						r.add(Event{Kind: EvProgress, Stage: idx, Done: done, Total: total})
					}
					r.mu.Unlock()
				},
			}
			if err = stages[idx].Run(ctx, st); err != nil {
				failed = idx
				break
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				failed = idx
				break
			}
		}
		r.mu.Lock()
		switch {
		case err == nil:
			r.state = StateDone
		case errors.Is(err, context.Canceled) || ctx.Err() != nil:
			r.state, r.err, r.failedAt = StateCanceled, "Cancelled.", failed
		default:
			r.state, r.err, r.failedAt = StateFailed, err.Error(), failed
		}
		r.add(Event{Kind: EvEnd, Stage: max(failed, 0), Text: r.state})
		r.mu.Unlock()
	}()
	return nil
}

// Cancel cancels the stage's context.
func (r *runner) Cancel() {
	r.mu.Lock()
	c := r.cancel
	r.mu.Unlock()
	if c != nil {
		c()
	}
}

// FailedAt is the index of the stage that failed or was cancelled, or -1.
func (r *runner) FailedAt() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedAt
}

// State is the run's state now.
func (r *runner) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Wait waits for the run's goroutine to finish.
func (r *runner) Wait() {
	r.mu.Lock()
	f := r.finished
	r.mu.Unlock()
	if f != nil {
		<-f
	}
}

func (r *runner) snapshotLocked(seen int) Snapshot {
	s := Snapshot{Seq: r.seq, State: r.state, Error: r.err, Stages: append([]string{}, r.names...), Events: []Event{}}
	if r.failedAt >= 0 && r.failedAt < len(r.names) {
		s.Failed = r.names[r.failedAt]
	}
	for _, e := range r.events {
		if e.Seq > seen {
			s.Events = append(s.Events, e)
		}
	}
	if r.progress != nil && r.progress.Seq > seen {
		p := *r.progress
		// keep order by seq
		i := len(s.Events)
		for i > 0 && s.Events[i-1].Seq > p.Seq {
			i--
		}
		s.Events = append(s.Events, Event{})
		copy(s.Events[i+1:], s.Events[i:])
		s.Events[i] = p
	}
	return s
}

// Snapshot is the run past seen, at once.
func (r *runner) Snapshot(seen int) Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(seen)
}

// Poll answers the moment an event past seen exists (or the run has ended
// or is not running), or when ctx ends, or after keepalive with nothing new.
func (r *runner) Poll(ctx context.Context, seen int, keepalive time.Duration) Snapshot {
	r.mu.Lock()
	ready := func() bool { return r.seq > seen || r.state != StateRunning }
	if ready() {
		defer r.mu.Unlock()
		return r.snapshotLocked(seen)
	}
	abandoned := false
	woke := make(chan struct{})
	go func() {
		r.mu.Lock()
		for !abandoned && !ready() {
			r.cond.Wait()
		}
		r.mu.Unlock()
		close(woke)
	}()
	r.mu.Unlock()

	t := time.NewTimer(keepalive)
	defer t.Stop()
	select {
	case <-woke:
	case <-ctx.Done():
	case <-t.C:
	}
	r.mu.Lock()
	abandoned = true
	r.mu.Unlock()
	r.cond.Broadcast()
	<-woke
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotLocked(seen)
}
