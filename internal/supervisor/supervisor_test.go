package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRun_AllServicesStartAndStop(t *testing.T) {
	var startedA, startedB int32
	svcs := []Service{
		{
			Name: "A",
			Start: func(ctx context.Context) error {
				atomic.AddInt32(&startedA, 1)
				<-ctx.Done()
				return nil
			},
		},
		{
			Name: "B",
			Start: func(ctx context.Context) error {
				atomic.AddInt32(&startedB, 1)
				<-ctx.Done()
				return nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, svcs) }()

	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), atomic.LoadInt32(&startedA))
	require.Equal(t, int32(1), atomic.LoadInt32(&startedB))

	cancel()
	require.NoError(t, <-done)
}

func TestRun_FirstFailureCancelsOthers(t *testing.T) {
	sentinel := errors.New("boom")
	var cancelled int32

	svcs := []Service{
		{
			Name: "fails",
			Start: func(ctx context.Context) error {
				return sentinel
			},
		},
		{
			Name: "watcher",
			Start: func(ctx context.Context) error {
				<-ctx.Done()
				atomic.StoreInt32(&cancelled, 1)
				return nil
			},
		},
	}

	err := Run(context.Background(), svcs)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, int32(1), atomic.LoadInt32(&cancelled))
}
