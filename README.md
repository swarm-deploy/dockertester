# dockertester

```shell
go get github.com/swarm-deploy/dockertester
```

**dockertester** - library for test Docker API

## Usage example

```go
package app

import (
	"context"
	"testing"
	"time"

	"github.com/swarm-deploy/dockertester"
	"github.com/stretchr/testify/require"
)

func Test(t *testing.T) {
	tester := dockertester.MustNewTester(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Fail early when the Docker daemon is not a Swarm manager.
	tester.Swarm().RequireSwarmMode(ctx, t)

	// Start the system under test, then wait for its stack and service.
	require.NoError(t, tester.Stack().WaitExists(ctx, "example"))
	require.NoError(t, tester.Service().WaitHealthy(ctx, "example_web"))

	// Stop the system under test, then verify that the stack disappears.
	require.NoError(t, tester.Stack().WaitRemoved(ctx, "example"))
}
```

Wait methods respect cancellation and deadlines from the supplied context. Service health can also use an explicit timeout when a separate context deadline is inconvenient:

```go
err := tester.Service().WaitHealthyTimeout(ctx, serviceID, 45*time.Second)
```

`MustNewTester` closes its Docker client through `t.Cleanup`. Resources are not removed automatically. Select cleanup explicitly and register it after creating the tester so it runs before the client is closed:

```go
serviceID, err := tester.Service().Deploy(ctx, spec)
require.NoError(t, err)

t.Cleanup(func() {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, tester.Service().DeleteAndWait(cleanupCtx, serviceID))
})
```
