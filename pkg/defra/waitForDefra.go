package defra

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	// DefraDBReadyMaxAttempts is the maximum number of attempts to wait for DefraDB to be ready.
	DefraDBReadyMaxAttempts = 15

	// DefraDBReadyRetryDelay is the delay between retry attempts when waiting for DefraDB.
	DefraDBReadyRetryDelay = 1 * time.Second

	// DefraDBReadyTimeout is the HTTP client timeout for health check requests.
	DefraDBReadyTimeout = 5 * time.Second

	// GraphQLEndpointPath is the path to the DefraDB GraphQL endpoint.
	GraphQLEndpointPath = "/api/v0/graphql"
)

// WaitForDefraDB waits for a DefraDB instance to be ready by checking the GraphQL endpoint.
// It retries until the endpoint responds successfully or until max attempts are reached.
func WaitForDefraDB(url string) error {
	fmt.Println("Waiting for defra...")

	graphqlURL := strings.TrimSuffix(url, "/") + GraphQLEndpointPath

	client := &http.Client{
		Timeout: DefraDBReadyTimeout,
	}

	// Simple GraphQL introspection query to check if DefraDB is ready
	// This doesn't require any specific schema to be applied
	query := `{"query":"{ __schema { types { name } } }"}`

	for attempt := 1; attempt <= DefraDBReadyMaxAttempts; attempt++ {
		// Create request
		req, err := http.NewRequestWithContext(
			context.Background(),
			"POST",
			graphqlURL,
			strings.NewReader(query),
		)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")

		// Make request
		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(DefraDBReadyRetryDelay)
			continue
		}

		// Check if response is successful
		if resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			fmt.Println("Defra is responsive!")
			return nil
		}
		fmt.Printf("Attempt %d failed... Trying again\n", attempt)

		_ = resp.Body.Close()
		time.Sleep(DefraDBReadyRetryDelay)
	}

	return fmt.Errorf("DefraDB failed to become ready after %d retry attempts", DefraDBReadyMaxAttempts)
}
