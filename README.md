# dockertester

```shell
go get github.com/swarm-deploy/dockertester
```

**dockertester** - library for test Docker API

## Usage Example

```go
package app

import (
	"context"
	"testing"

	"github.com/swarm-deploy/dockertester"
)

func Test(t *testing.T) {
	tester := dockertester.MustNewTester(t)

	tester.Swarm().RequireSwarmMode(context.Background(), t)
}
```

## Swarm service observations

The existing `ServiceTester` provides context-bounded observations:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
ids, err := tester.Service().WaitRunningTasks(ctx, serviceID, 2)
// Check err; ids contains exactly two sorted, current-and-desired running tasks.
baseline, err := tester.Service().WaitStable(ctx, serviceID, time.Second)
// Check err, and inspect baseline.Service for the expected rollout outcome.
// Perform the operation under test here.
err = tester.Service().WaitUnchanged(ctx, baseline, 2*time.Second)
// Check err: no service update, replacement or additional task was observed.
```

- `Snapshot(ctx, serviceID)` captures the service and all retained tasks, sorted
  by ID. It brackets task listing with service inspections to avoid accepting a
  baseline across a concurrent service update. Swarm reads are not transactional.
- `WaitRunningTasks` checks exact current/desired running counts, including extra
  running tasks still stopping during scale-down. Terminal history is ignored.
- `WaitStable` requires continuously quiet service version, specification,
  rollout metadata and task observations. Updating/rolling-back states keep
  waiting; paused rollouts return the observed snapshot and an error. Settled
  rollback and failed/completed tasks can be quiet: stability is not health.
- `WaitUnchanged` compares against the supplied baseline for the full interval,
  failing immediately on observed service metadata, task identity or task-state
  changes. New terminal tasks also fail; removal of old terminal history is
  allowed, including transitions between terminal cleanup states. Capture a fresh baseline after an expected job completion.

Observations poll approximately every 100ms. Tasks created and garbage-collected
entirely between reads cannot be detected. Use the caller's context for deadlines;
these methods impose no fixed global timeout. Snapshots are immutable baselines:
do not modify their service or task data after capture.
