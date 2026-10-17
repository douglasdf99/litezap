// Package spawner starts and stops instance workers.
package spawner

import (
	"context"

	workerv1 "github.com/douglasdf99/litezap/gen/worker/v1"
)

// WorkerHandle controls one running worker.
type WorkerHandle interface {
	Client() workerv1.WorkerClient
	// Done is closed when the worker has exited.
	Done() <-chan struct{}
	// Kill stops the worker immediately and waits for it to exit.
	Kill() error
	// Close asks the worker to stop gracefully, killing it if ctx expires first.
	Close(ctx context.Context) error
}

// Spawner starts workers. ProcessSpawner runs local processes; a container
// implementation arrives with the cloud piece.
type Spawner interface {
	Start(ctx context.Context, instanceID string) (WorkerHandle, error)
}
