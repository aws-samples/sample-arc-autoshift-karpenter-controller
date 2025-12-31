package utils

import (
	"context"
	"log"
	"sync"
)

// BatchSize defines how many items to process in a batch
const BatchSize = 20

// MaxConcurrentBatches defines the maximum number of batches to process concurrently
const MaxConcurrentBatches = 5

// BatchProcessor processes items in batches with limited concurrency
// T is a generic type parameter that allows this function to work with any type
func BatchProcessor[T any](
	ctx context.Context,
	items []T,
	processFn func(ctx context.Context, batch []T) error,
) error {
	if len(items) == 0 {
		log.Printf("[BatchProcessor] No items to process")
		return nil
	}

	// Calculate the number of batches
	totalItems := len(items)
	batchCount := (totalItems + BatchSize - 1) / BatchSize
	log.Printf("[BatchProcessor] Processing %d items in %d batches (batch size: %d)", totalItems, batchCount, BatchSize)

	// Create a semaphore to limit concurrency
	semaphore := make(chan struct{}, MaxConcurrentBatches)
	var wg sync.WaitGroup
	errChan := make(chan error, batchCount)

	// Process each batch
	for i := 0; i < batchCount; i++ {
		wg.Add(1)

		// Calculate batch range
		start := i * BatchSize
		end := start + BatchSize
		if end > totalItems {
			end = totalItems
		}

		// Get the current batch of items
		batch := items[start:end]

		// Launch goroutine to process batch
		go func(batchNum int, batch []T) {
			defer wg.Done()
			log.Printf("[BatchProcessor] Starting batch %d with %d items", batchNum, len(batch))

			// Check if context is already done
			if ctx.Err() != nil {
				log.Printf("[BatchProcessor] Context cancelled for batch %d", batchNum)
				errChan <- ctx.Err()
				return
			}

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				// Continue processing
				defer func() { <-semaphore }() // Release semaphore when done
			case <-ctx.Done():
				log.Printf("[BatchProcessor] Context cancelled while waiting for semaphore (batch %d)", batchNum)
				errChan <- ctx.Err()
				return
			}

			// Process the batch with context check
			select {
			case <-ctx.Done():
				log.Printf("[BatchProcessor] Context cancelled before processing batch %d", batchNum)
				errChan <- ctx.Err()
				return
			default:
				// Continue processing
				if err := processFn(ctx, batch); err != nil {
					log.Printf("[BatchProcessor] Error processing batch %d: %v", batchNum, err)
					select {
					case errChan <- err:
						// Error sent
					default:
						// Channel buffer full, log the error
						log.Printf("[BatchProcessor] Error channel full, batch %d error: %v", batchNum, err)
					}
					return
				}
				log.Printf("[BatchProcessor] Completed batch %d", batchNum)
			}
		}(i, batch)
	}

	// Wait for all batches to complete
	wg.Wait()
	close(errChan)
	log.Printf("[BatchProcessor] All batches completed")

	// Check if there were any errors
	for err := range errChan {
		if err != nil {
			log.Printf("[BatchProcessor] Returning error: %v", err)
			return err
		}
	}

	log.Printf("[BatchProcessor] Successfully processed all %d items", totalItems)
	return nil
}