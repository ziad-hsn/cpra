//go:build externaljobs

package management

import "context"

// Startup authenticates retained ciphertext independently of current dispatch
// eligibility. Expired and previous-owner intents still require their old keys.
func (c *Catalog) verifyWorkerExecutions(ctx context.Context) error {
	after := ""
	for {
		page, err := c.store.WorkerExecutions(ctx, after, 100)
		if err != nil {
			return workerPreparationError(ctx, err)
		}
		for _, record := range page.Items {
			if _, err := c.openWorkerExecution(ctx, record); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				c.fail()
				return ErrUnavailable
			}
		}
		if !page.More {
			return ctx.Err()
		}
		if page.Next <= after || len(page.Items) == 0 {
			c.fail()
			return ErrUnavailable
		}
		after = page.Next
	}
}
