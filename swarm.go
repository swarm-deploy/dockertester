package dockertester

import (
	"context"
	"fmt"
	"testing"

	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

type SwarmTester struct {
	client dock.APIClient
}

func NewSwarmTester(client dock.APIClient) *SwarmTester {
	return &SwarmTester{client: client}
}

func (st *SwarmTester) RequireSwarmMode(ctx context.Context, t *testing.T) {
	t.Helper()

	_, err := st.client.SwarmInspect(ctx, dock.SwarmInspectOptions{})
	require.NoError(t, err, "Docker Swarm must be initialized before running e2e tests")
}

func (st *SwarmTester) Inspect(ctx context.Context) (dock.SwarmInspectResult, error) {
	info, err := st.client.SwarmInspect(ctx, dock.SwarmInspectOptions{})
	if err != nil {
		return dock.SwarmInspectResult{}, fmt.Errorf("inspect swarm: %w", err)
	}

	return info, nil
}
