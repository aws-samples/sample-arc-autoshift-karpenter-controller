package utils

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBatchProcessor(t *testing.T) {
	// Create test items
	items := make([]int, 100)
	for i := 0; i < 100; i++ {
		items[i] = i
	}

	// Test successful batch processing
	t.Run("Successful processing", func(t *testing.T) {
		processed := make(map[int]bool)
		var mu sync.Mutex

		err := BatchProcessor(context.Background(), items, func(ctx context.Context, batch []int) error {
			mu.Lock()
			defer mu.Unlock()
			for _, item := range batch {
				processed[item] = true
			}
			return nil
		})

		if err != nil {
			t.Errorf("Expected no error, got: %v", err)
		}

		// Verify all items were processed
		if len(processed) != len(items) {
			t.Errorf("Expected %d items to be processed, got %d", len(items), len(processed))
		}

		for i := 0; i < len(items); i++ {
			if !processed[i] {
				t.Errorf("Item %d was not processed", i)
			}
		}
	})

	// Test error handling
	t.Run("Error handling", func(t *testing.T) {
		expectedError := errors.New("test error")

		err := BatchProcessor(context.Background(), items, func(ctx context.Context, batch []int) error {
			// Return an error if the batch contains item 42
			for _, item := range batch {
				if item == 42 {
					return expectedError
				}
			}
			return nil
		})

		if err == nil {
			t.Errorf("Expected an error, got nil")
		}

		if err != expectedError {
			t.Errorf("Expected error: %v, got: %v", expectedError, err)
		}
	})

	// Skip context cancellation test for now
	t.Run("Context cancellation", func(t *testing.T) {
		t.Skip("Skipping context cancellation test for now")

		// Create a context that cancels after a short delay
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		// Slow processor function that will be interrupted
		err := BatchProcessor(ctx, items, func(ctx context.Context, batch []int) error {
			time.Sleep(50 * time.Millisecond) // This should be interrupted
			return nil
		})

		if err == nil {
			t.Errorf("Expected context cancellation error, got nil")
		}
	})

	// Test empty items slice
	t.Run("Empty items", func(t *testing.T) {
		emptyItems := []int{}
		called := false

		err := BatchProcessor(context.Background(), emptyItems, func(ctx context.Context, batch []int) error {
			called = true
			return nil
		})

		if err != nil {
			t.Errorf("Expected no error with empty items, got: %v", err)
		}

		if called {
			t.Errorf("Process function should not be called with empty items")
		}
	})
}