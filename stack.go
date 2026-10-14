package dockertester

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
)

const stackNamespaceLabel = "com.docker.stack.namespace"

type StackTester struct {
	client dock.APIClient
}

func NewStackTester(client dock.APIClient) *StackTester {
	return &StackTester{client: client}
}

func (st *StackTester) Services(ctx context.Context, stackName string) ([]swarm.Service, error) {
	services, err := st.client.ServiceList(ctx, dock.ServiceListOptions{
		Filters: dock.Filters{}.Add("label", stackNamespaceLabel+"="+stackName),
	})
	if err != nil {
		return nil, fmt.Errorf("list services for stack %q: %w", stackName, err)
	}

	matched := make([]swarm.Service, 0, len(services.Items))
	for _, service := range services.Items {
		if service.Spec.Labels[stackNamespaceLabel] == stackName {
			matched = append(matched, service)
		}
	}

	return matched, nil
}

func (st *StackTester) WaitExists(ctx context.Context, stackName string) error {
	return st.wait(ctx, stackName, true)
}

func (st *StackTester) WaitRemoved(ctx context.Context, stackName string) error {
	return st.wait(ctx, stackName, false)
}

func (st *StackTester) wait(ctx context.Context, stackName string, exists bool) error {
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()

	var lastServices []swarm.Service
	for {
		services, err := st.Services(ctx, stackName)
		if err != nil {
			return err
		}
		lastServices = services
		if (len(services) > 0) == exists {
			return nil
		}

		select {
		case <-ctx.Done():
			state := "exist"
			if !exists {
				state = "be removed"
			}

			return fmt.Errorf(
				"wait for stack %q to %s: %w; matching services: %s",
				stackName,
				state,
				ctx.Err(),
				formatServices(lastServices),
			)
		case <-ticker.C:
		}
	}
}

func formatServices(services []swarm.Service) string {
	if len(services) == 0 {
		return "none"
	}

	formatted := make([]string, 0, len(services))
	for _, service := range services {
		formatted = append(formatted, fmt.Sprintf("%s(%s)", service.Spec.Name, service.ID))
	}

	return strings.Join(formatted, ", ")
}
