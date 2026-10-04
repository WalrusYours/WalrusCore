package learn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

var (
	// ErrRunning is returned by Start while a run is still in progress.
	ErrRunning = errors.New("learn: a training run is already in progress")
	// ErrNoSchema is returned when no schema has been pushed yet.
	ErrNoSchema = errors.New("learn: no schema has been pushed yet")
	// ErrNothingToTrain is returned when the schema declares no embedding signal.
	ErrNothingToTrain = errors.New("learn: the schema declares no embedding signal")
	// ErrUnknownSignal is returned for a signal id that is not an embedding signal of the schema.
	ErrUnknownSignal = errors.New("learn: not an embedding signal of the current schema")
)

// Job is the state of the latest training run, which stays readable until the next one starts.
type Job struct {
	Running    bool       `json:"running"`
	Trigger    string     `json:"trigger,omitempty"` // what started it: "api", "schedule"
	Signals    []string   `json:"signals,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Results    []Result   `json:"results,omitempty"`
	// Error is set when the run itself failed (a crash); a signal that could not be trained is
	// reported in its own Result.
	Error string `json:"error,omitempty"`
}

// Runner trains models in the background, one run at a time. Training a large catalogue takes far
// longer than a request may, so a caller starts a run and reads its Job.
type Runner struct {
	st       store.Store
	compiled func() *schema.Compiled
	now      func() time.Time

	mu  sync.Mutex
	job Job
	wg  sync.WaitGroup
}

// NewRunner trains against the store and the schema compiled returns at the time a run starts.
func NewRunner(st store.Store, compiled func() *schema.Compiled) *Runner {
	return &Runner{st: st, compiled: compiled, now: time.Now}
}

// Start begins a run in the background and returns at once. It trains the named embedding signals,
// or all of them when none is named. trigger says why, for the status ("api", "schedule").
func (r *Runner) Start(trigger string, signals ...string) error {
	c := r.compiled()
	if c == nil {
		return ErrNoSchema
	}
	all := Signals(c.Schema)
	if len(all) == 0 {
		return ErrNothingToTrain
	}
	ids := signals
	if len(ids) == 0 {
		ids = all
	}
	for _, id := range ids {
		if sg, ok := c.Schema.Signals[id]; !ok || sg.Type != SignalType {
			return fmt.Errorf("%w: %q", ErrUnknownSignal, id)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job.Running {
		return ErrRunning
	}
	started := r.now().UTC()
	r.job = Job{Running: true, Trigger: trigger, Signals: append([]string(nil), ids...), StartedAt: &started}
	r.wg.Add(1)
	go r.run(c, ids)
	return nil
}

func (r *Runner) run(c *schema.Compiled, ids []string) {
	defer r.wg.Done()
	var results []Result
	var failure string
	defer func() {
		if p := recover(); p != nil { // a crash must not leave the runner stuck as running
			failure = fmt.Sprint("training crashed: ", p)
			slog.Error("learn: training crashed", "panic", p)
		}
		finished := r.now().UTC()
		r.mu.Lock()
		r.job.Running, r.job.FinishedAt, r.job.Results, r.job.Error = false, &finished, results, failure
		r.mu.Unlock()
	}()
	results, _ = trainEach(context.Background(), r.st, c, ids, r.now())
	for _, res := range results {
		if res.Error != "" {
			slog.Warn("learn: a signal was not trained", "signal", res.Signal, "reason", res.Error)
		} else {
			slog.Info("learn: trained", "signal", res.Signal, "version", res.Version, "items", res.Items,
				"users", res.Users, "interactions", res.Interactions, "took_ms", res.TookMS)
		}
	}
}

// Job returns the latest run's state.
func (r *Runner) Job() Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.job
	j.Results = append([]Result(nil), r.job.Results...)
	j.Signals = append([]string(nil), r.job.Signals...)
	return j
}

// Wait blocks until the run in progress, if any, has finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Every starts a run each interval until ctx ends. A tick that finds a run still going, no schema, or
// nothing to train is skipped quietly.
func (r *Runner) Every(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			switch err := r.Start("schedule"); {
			case err == nil, errors.Is(err, ErrRunning), errors.Is(err, ErrNoSchema), errors.Is(err, ErrNothingToTrain):
			default:
				slog.Error("learn: could not start a scheduled run", "err", err)
			}
		}
	}
}
