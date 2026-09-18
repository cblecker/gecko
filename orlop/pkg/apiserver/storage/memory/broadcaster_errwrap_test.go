package memory

import (
	"errors"
	"strconv"
	"testing"
)

// TestGetEventsSince_InvalidRV_ErrorWraps verifies that the error returned
// for an invalid resourceVersion wraps the underlying strconv.NumError with
// %w, so callers can inspect the cause via errors.Is / errors.As.
func TestGetEventsSince_InvalidRV_ErrorWraps(t *testing.T) {
	buf := NewWatchBuffer(10)
	buf.Add(makeEvent("ADDED", "1"))

	_, err := buf.GetEventsSince("not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid RV, got nil")
	}

	// The underlying error must be a *strconv.NumError.
	var numErr *strconv.NumError
	if !errors.As(err, &numErr) {
		t.Errorf("expected error to wrap *strconv.NumError, got: %v", err)
	}

	// errors.Is must also work for the specific sentinel.
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Errorf("expected errors.Is(err, strconv.ErrSyntax) to be true, got false; err = %v", err)
	}
}
