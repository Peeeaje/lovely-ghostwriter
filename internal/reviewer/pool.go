package reviewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

type Pool struct {
	config  config.Config
	store   *state.Store
	runner  *Runner
	output  io.Writer
	sem     chan struct{}
	wg      sync.WaitGroup
	errorMu sync.Mutex
	errors  []error
}

func NewPool(cfg config.Config, store *state.Store, runner *Runner, output io.Writer) *Pool {
	return &Pool{
		config: cfg,
		store:  store,
		runner: runner,
		output: output,
		sem:    make(chan struct{}, cfg.Daemon.MaxConcurrent),
	}
}

func (p *Pool) StartAvailable(ctx context.Context) (int, error) {
	started := 0
	for len(p.sem) < cap(p.sem) {
		pr, run, ok, err := p.store.ClaimNext(ctx)
		if err != nil {
			return started, err
		}
		if !ok {
			return started, nil
		}
		repository, ok := p.repository(pr.Repository)
		if !ok {
			err := fmt.Errorf("repository %s is no longer configured", pr.Repository)
			_ = p.store.FinishRun(ctx, run, state.StatusFailed, err)
			continue
		}
		posted, err := p.runner.RecoveredReviewPosted(ctx, repository, pr)
		if err != nil {
			_ = p.store.FinishRun(ctx, run, state.StatusFailed, err)
			p.recordError(err)
			continue
		}
		if posted {
			_ = p.store.FinishRun(ctx, run, state.StatusReviewed, nil)
			fmt.Fprintf(p.output, "recovered posted review %s#%d run=%d\n", pr.Repository, pr.Number, run.ID)
			continue
		}
		if !pr.Manual {
			prepared, eligible, err := p.runner.PrepareAutomatic(ctx, repository, pr)
			if err != nil {
				_ = p.store.FinishRun(ctx, run, state.StatusFailed, err)
				p.recordError(err)
				fmt.Fprintf(p.output, "failed to validate %s#%d run=%d: %v\n", pr.Repository, pr.Number, run.ID, err)
				continue
			}
			if !eligible {
				_ = p.store.FinishRun(ctx, run, state.StatusCanceled, nil)
				fmt.Fprintf(p.output, "canceled stale automatic queue %s#%d run=%d\n", pr.Repository, pr.Number, run.ID)
				continue
			}
			pr = prepared
			if err := p.store.SetRunningTarget(ctx, pr); err != nil {
				_ = p.store.FinishRun(ctx, run, state.StatusFailed, err)
				p.recordError(err)
				continue
			}
		}

		p.sem <- struct{}{}
		p.wg.Add(1)
		started++
		fmt.Fprintf(p.output, "started %s#%d run=%d\n", pr.Repository, pr.Number, run.ID)
		go func() {
			defer func() {
				<-p.sem
				p.wg.Done()
			}()
			if err := p.runner.NotifyStarted(context.Background(), pr); err != nil {
				fmt.Fprintf(p.output, "notification warning: %v\n", err)
			}
			finishedRun, finishedPR, status, runErr := p.runner.Run(ctx, repository, pr, run)
			status, err := p.store.FinishRunStatus(context.Background(), finishedRun, status, runErr)
			if err != nil {
				p.recordError(err)
				fmt.Fprintf(p.output, "failed to persist %s#%d run=%d: %v\n", pr.Repository, pr.Number, run.ID, err)
				return
			}
			if err := p.runner.NotifyFinished(context.Background(), finishedPR, status, runErr); err != nil {
				fmt.Fprintf(p.output, "notification warning: %v\n", err)
			}
			if runErr != nil {
				p.recordError(runErr)
				fmt.Fprintf(p.output, "failed %s#%d run=%d: %v\n", pr.Repository, pr.Number, run.ID, runErr)
				return
			}
			fmt.Fprintf(p.output, "finished %s#%d run=%d status=%s\n", pr.Repository, pr.Number, run.ID, status)
		}()
	}
	return started, nil
}

func (p *Pool) Wait() {
	p.wg.Wait()
}

func (p *Pool) Drain(ctx context.Context) error {
	for {
		started, err := p.StartAvailable(ctx)
		if err != nil {
			p.Wait()
			return errors.Join(append([]error{err}, p.takeErrors()...)...)
		}
		p.Wait()
		if started == 0 {
			return errors.Join(p.takeErrors()...)
		}
	}
}

func (p *Pool) recordError(err error) {
	p.errorMu.Lock()
	defer p.errorMu.Unlock()
	p.errors = append(p.errors, err)
}

func (p *Pool) takeErrors() []error {
	p.errorMu.Lock()
	defer p.errorMu.Unlock()
	errs := p.errors
	p.errors = nil
	return errs
}

func (p *Pool) repository(name string) (config.RepositoryConfig, bool) {
	for _, repository := range p.config.Repositories {
		if repository.Name == name {
			return repository, true
		}
	}
	return config.RepositoryConfig{}, false
}
