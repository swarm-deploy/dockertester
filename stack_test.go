package dockertester

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestStackServicesMatchesNamespaceExactly(t *testing.T) {
	client := &fakeDockerClient{
		serviceList: func(_ context.Context, options dock.ServiceListOptions) (dock.ServiceListResult, error) {
			require.True(t, options.Filters["label"][stackNamespaceLabel+"=app"])

			return dock.ServiceListResult{Items: []swarm.Service{
				stackService("one", "app_web", "app"),
				stackService("two", "application_web", "application"),
				stackService("three", "unmanaged", ""),
			}}, nil
		},
	}

	services, err := NewStackTester(client).Services(context.Background(), "app")
	require.NoError(t, err)
	require.Len(t, services, 1)
	require.Equal(t, "one", services[0].ID)
}

func TestStackWaitExists(t *testing.T) {
	calls := 0
	client := &fakeDockerClient{
		serviceList: func(context.Context, dock.ServiceListOptions) (dock.ServiceListResult, error) {
			calls++
			if calls == 1 {
				return dock.ServiceListResult{}, nil
			}

			return dock.ServiceListResult{Items: []swarm.Service{
				stackService("one", "app_web", "app"),
			}}, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, NewStackTester(client).WaitExists(ctx, "app"))
	require.GreaterOrEqual(t, calls, 2)
}

func TestStackWaitRemoved(t *testing.T) {
	calls := 0
	client := &fakeDockerClient{
		serviceList: func(context.Context, dock.ServiceListOptions) (dock.ServiceListResult, error) {
			calls++
			if calls == 1 {
				return dock.ServiceListResult{Items: []swarm.Service{
					stackService("one", "app_web", "app"),
				}}, nil
			}

			return dock.ServiceListResult{}, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, NewStackTester(client).WaitRemoved(ctx, "app"))
	require.GreaterOrEqual(t, calls, 2)
}

func TestStackWaitHonorsContext(t *testing.T) {
	client := &fakeDockerClient{
		serviceList: func(context.Context, dock.ServiceListOptions) (dock.ServiceListResult, error) {
			return dock.ServiceListResult{}, nil
		},
	}
	tester := NewStackTester(client)

	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := tester.WaitExists(ctx, "app")
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "matching services: none")
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()

		err := tester.WaitExists(ctx, "app")
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestStackServicesReturnsDockerError(t *testing.T) {
	dockerErr := errors.New("daemon unavailable")
	client := &fakeDockerClient{
		serviceList: func(context.Context, dock.ServiceListOptions) (dock.ServiceListResult, error) {
			return dock.ServiceListResult{}, dockerErr
		},
	}

	_, err := NewStackTester(client).Services(context.Background(), "app")
	require.ErrorIs(t, err, dockerErr)
	require.ErrorContains(t, err, `list services for stack "app"`)
}

func stackService(id, name, stackName string) swarm.Service {
	labels := map[string]string{}
	if stackName != "" {
		labels[stackNamespaceLabel] = stackName
	}

	return swarm.Service{
		ID: id,
		Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{
			Name:   name,
			Labels: labels,
		}},
	}
}
