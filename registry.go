package pussh

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	registryReadyTimeout  = 30 * time.Second
	registryReadyInterval = 100 * time.Millisecond
)

func waitForRegistry(ctx context.Context, port int) error {
	url := fmt.Sprintf("http://localhost:%d/v2/", port)
	client := &http.Client{
		Timeout: 1 * time.Second,
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, registryReadyTimeout)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("registry not ready after %s: %w", registryReadyTimeout, timeoutCtx.Err())
		default:
		}

		req, err := http.NewRequestWithContext(timeoutCtx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		time.Sleep(registryReadyInterval)
	}
}
