// Package supervisor runs a set of long-lived services under a shared context
// and propagates the first error as the group failure.
package supervisor

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// Service is a named long-lived goroutine. Start must block until ctx is
// cancelled, unless it returns an error. A nil error on return is treated as
// an orderly shutdown.
type Service struct {
	Name  string
	Start func(ctx context.Context) error
}

// Run starts all services concurrently. Returns when:
//   - ctx is cancelled (returns nil — clean external cancel is not an error), or
//   - any service returns a non-nil error (other services are signalled to
//     stop via context cancellation, and the first error is returned).
func Run(ctx context.Context, services []Service) error {
	g, gctx := errgroup.WithContext(ctx)

	for _, svc := range services {
		if svc.Start == nil {
			return fmt.Errorf("supervisor: service %q has nil Start", svc.Name)
		}
		g.Go(func() error {
			if err := svc.Start(gctx); err != nil {
				return fmt.Errorf("%s: %w", svc.Name, err)
			}
			return nil
		})
	}

	// Clean external cancel is not an error; the group returns nil.
	return g.Wait()
}
