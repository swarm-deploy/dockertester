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
