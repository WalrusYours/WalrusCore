package learn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/timurcravtov/walrus/internal/domain"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store"
)

// gate holds a training run inside the store until it is released, so a test can look at a run
// that is in progress.
type gate struct {
	store.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate(st store.Store) *gate {
	return &gate{Store: st, started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gate) ListEntities(ctx context.Context, typ string) ([]domain.Entity, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return g.Store.ListEntities(ctx, typ)
}

func fixedSchema(c *schema.Compiled) func() *schema.Compiled {
	return func() *schema.Compiled { return c }
}

func TestStartTrainsInTheBackgroundAndRemembersTheRun(t *testing.T) {
	st, c := twoCrowds(t), compile(t, base)
	r := NewRunner(st, fixedSchema(c))
	if j := r.Job(); j.Running || j.StartedAt != nil || len(j.Results) != 0 {
		t.Errorf("before any run: %+v", j)
	}

	if err := r.Start("api"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	j := r.Job()
	if j.Running || j.Trigger != "api" || j.StartedAt == nil || j.FinishedAt == nil || j.Error != "" ||
		len(j.Results) != 1 || j.Results[0].Signal != "taste" || j.Results[0].Version != 1 || j.Results[0].Error != "" {
		t.Errorf("job = %+v", j)
	}
	if j.FinishedAt.Before(*j.StartedAt) {
		t.Errorf("finished %v before it started %v", j.FinishedAt, j.StartedAt)
	}
	if m, err := st.Model(context.Background(), "taste"); err != nil || m.Version != 1 {
		t.Errorf("model = %+v, %v", m, err)
	}

	// the next run replaces the remembered one
	if err := r.Start("schedule"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if j := r.Job(); j.Trigger != "schedule" || j.Results[0].Version != 2 {
		t.Errorf("second job = %+v", j)
	}
}

func TestOnlyOneRunAtATime(t *testing.T) {
	g := newGate(twoCrowds(t))
	r := NewRunner(g, fixedSchema(compile(t, base)))

	if err := r.Start("api"); err != nil {
		t.Fatal(err)
	}
	<-g.started
	if j := r.Job(); !j.Running || j.FinishedAt != nil || j.StartedAt == nil {
		t.Errorf("a run in progress reports %+v", j)
	}
	if err := r.Start("api"); !errors.Is(err, ErrRunning) {
		t.Errorf("a second Start during a run = %v, want ErrRunning", err)
	}
	close(g.release)
	r.Wait()
	if j := r.Job(); j.Running || len(j.Results) != 1 || j.Results[0].Version != 1 {
		t.Errorf("after the run: %+v", j)
	}
	if err := r.Start("api"); err != nil {
		t.Errorf("a new run after the first finished = %v", err)
	}
	r.Wait()
}

func TestStartRefusesWhatCannotBeTrained(t *testing.T) {
	st := twoCrowds(t)
	if err := NewRunner(st, func() *schema.Compiled { return nil }).Start("api"); !errors.Is(err, ErrNoSchema) {
		t.Errorf("no schema: %v", err)
	}
	none := compile(t, strings.Replace(base, "type: embedding, for: film, factors: 4, regularization: 0.1, alpha: 10, iterations: 10,", "type: global_count,", 1))
	if err := NewRunner(st, fixedSchema(none)).Start("api"); !errors.Is(err, ErrNothingToTrain) {
		t.Errorf("no embedding signal: %v", err)
	}
	r := NewRunner(st, fixedSchema(compile(t, base)))
	for _, id := range []string{"nope", "popularity"} {
		if err := r.Start("api", id); !errors.Is(err, ErrUnknownSignal) {
			t.Errorf("Start(%q) = %v, want ErrUnknownSignal", id, err)
		}
	}
	if j := r.Job(); j.Running || j.StartedAt != nil {
		t.Errorf("a refused start must not leave a run behind: %+v", j)
	}
}

func TestStartTrainsOnlyTheNamedSignals(t *testing.T) {
	text := strings.Replace(base, "recommendable: film", "  mood:  { type: embedding, for: film, factors: 2, default: 0.2 }\nrecommendable: film", 1)
	st, c := twoCrowds(t), compile(t, text)
	r := NewRunner(st, fixedSchema(c))

	if err := r.Start("api", "mood"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if _, err := st.Model(context.Background(), "mood"); err != nil {
		t.Errorf("the named signal was not trained: %v", err)
	}
	if _, err := st.Model(context.Background(), "taste"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a signal that was not named was trained: %v", err)
	}
	if j := r.Job(); len(j.Signals) != 1 || j.Signals[0] != "mood" {
		t.Errorf("job signals = %v", j.Signals)
	}

	if err := r.Start("api"); err != nil { // none named: every embedding signal
		t.Fatal(err)
	}
	r.Wait()
	if j := r.Job(); len(j.Results) != 2 {
		t.Errorf("all signals: %+v", j.Results)
	}
}

func TestAnUntrainableSignalIsReportedNotFatal(t *testing.T) {
	// a catalogue with no interactions yet
	r := NewRunner(newEmptyStore(t), fixedSchema(compile(t, base)))
	if err := r.Start("api"); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	j := r.Job()
	if j.Running || j.Error != "" || len(j.Results) != 1 || !strings.Contains(j.Results[0].Error, "no interactions") {
		t.Errorf("job = %+v", j)
	}
}

func TestEveryStartsRunsUntilTheContextEnds(t *testing.T) {
	st, c := twoCrowds(t), compile(t, base)
	r := NewRunner(st, fixedSchema(c))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Every(ctx, 5*time.Millisecond); close(done) }()

	deadline := time.After(5 * time.Second)
	for {
		if m, err := st.Model(context.Background(), "taste"); err == nil && m.Version >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the schedule did not run twice")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
	r.Wait()
	v1, _ := st.Model(context.Background(), "taste")
	time.Sleep(40 * time.Millisecond)
	if v2, _ := st.Model(context.Background(), "taste"); v2.Version != v1.Version {
		t.Errorf("runs continued after the context ended: version %d then %d", v1.Version, v2.Version)
	}
	if j := r.Job(); j.Trigger != "schedule" {
		t.Errorf("trigger = %q", j.Trigger)
	}
	r.Every(context.Background(), 0) // a zero interval does nothing and returns at once
}
