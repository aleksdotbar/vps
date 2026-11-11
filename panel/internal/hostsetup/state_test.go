package hostsetup

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestDetachedSetupWaitsForObservationLockAndHonorsCancellation(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	held, err := stateLock(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if file, err := waitStateLock(ctx, root); file != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended cancellation acquired lock or lost deadline: file=%v error=%v", file, err)
	}
	if file, err := stateLock(root); file != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("read-only callers no longer refuse contention: file=%v error=%v", file, err)
	}
	result := make(chan error, 1)
	waitCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	go func() {
		file, err := waitStateLock(waitCtx, root)
		if file != nil {
			err = file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("runner abandoned still-held observation lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("runner did not acquire released observation lock: %v", err)
	}
}
