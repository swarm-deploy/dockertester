package dockertester

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
)

const waitServiceHealthyTimeout = 15 * time.Second

type ServiceTester struct {
	client dock.APIClient
}

func NewServiceTester(client dock.APIClient) *ServiceTester {
	return &ServiceTester{
		client: client,
	}
}

func (st *ServiceTester) Deploy(ctx context.Context, spec swarm.ServiceSpec) (string, error) {
	created, err := st.client.ServiceCreate(ctx, dock.ServiceCreateOptions{Spec: spec})
	if err != nil {
		return "", fmt.Errorf("deploy service %q: %w", spec.Name, err)
	}

	return created.ID, nil
}

func (st *ServiceTester) WaitHealthy(ctx context.Context, serviceID string) error {
	ctx, cancel := context.WithTimeout(ctx, waitServiceHealthyTimeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastStatus string
	for {
		healthy, status, err := st.Healthy(ctx, serviceID)
		if err != nil {
			return err
		}
		if healthy {
			return nil
		}
		lastStatus = status

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait service %q healthy: %w: %s", serviceID, ctx.Err(), lastStatus)
		case <-ticker.C:
		}
	}
}

func (st *ServiceTester) Healthy(ctx context.Context, serviceID string) (bool, string, error) {
	service, err := st.client.ServiceInspect(ctx, serviceID, dock.ServiceInspectOptions{})
	if err != nil {
		return false, "", fmt.Errorf("inspect service %q: %w", serviceID, err)
	}

	tasks, err := st.client.TaskList(ctx, dock.TaskListOptions{
		Filters: dock.Filters{}.Add("service", serviceID),
	})
	if err != nil {
		return false, "", fmt.Errorf("list tasks for service %q: %w", serviceID, err)
	}

	expectedState := swarm.TaskStateRunning
	if st.isCompletionService(service.Service.Spec) {
		expectedState = swarm.TaskStateComplete
	}

	var healthy int
	statuses := make([]string, 0, len(tasks.Items))
	for _, task := range tasks.Items {
		statuses = append(statuses, fmt.Sprintf("%s:%s:%s", task.ID, task.Status.State, task.Status.Err))

		if task.Status.State == swarm.TaskStateRejected || task.Status.State == swarm.TaskStateFailed {
			return false, strings.Join(statuses, ", "), fmt.Errorf(
				"service %q task %q failed: state=%s err=%s",
				service.Service.Spec.Name,
				task.ID,
				task.Status.State,
				task.Status.Err,
			)
		}
		if task.Status.State == expectedState {
			healthy++
		}
	}

	desired := st.desiredTasks(service.Service.Spec)
	if healthy >= desired {
		return true, strings.Join(statuses, ", "), nil
	}

	return false, strings.Join(statuses, ", "), nil
}

func (st *ServiceTester) WaitRemoved(ctx context.Context, serviceID string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		_, err := st.client.ServiceInspect(ctx, serviceID, dock.ServiceInspectOptions{})
		if err != nil {
			if errdefs.IsNotFound(err) {
				return nil
			}

			return fmt.Errorf("inspect removed service %q: %w", serviceID, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait service %q removed: %w", serviceID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (*ServiceTester) desiredTasks(spec swarm.ServiceSpec) int {
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		return int(*spec.Mode.Replicated.Replicas)
	}
	if spec.Mode.ReplicatedJob != nil {
		if spec.Mode.ReplicatedJob.TotalCompletions != nil {
			return int(*spec.Mode.ReplicatedJob.TotalCompletions)
		}
		if spec.Mode.ReplicatedJob.MaxConcurrent != nil {
			return int(*spec.Mode.ReplicatedJob.MaxConcurrent)
		}
	}

	return 1
}

func (*ServiceTester) isCompletionService(spec swarm.ServiceSpec) bool {
	if spec.Mode.ReplicatedJob != nil || spec.Mode.GlobalJob != nil {
		return true
	}

	return spec.TaskTemplate.RestartPolicy != nil &&
		spec.TaskTemplate.RestartPolicy.Condition == swarm.RestartPolicyConditionNone
}