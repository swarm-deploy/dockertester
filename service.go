package dockertester

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
)

const waitPollInterval = 500 * time.Millisecond

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
	return st.waitHealthy(ctx, serviceID)
}

func (st *ServiceTester) WaitHealthyTimeout(
	ctx context.Context,
	serviceID string,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		return fmt.Errorf("wait service %q healthy: timeout must be positive", serviceID)
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return st.waitHealthy(waitCtx, serviceID)
}

func (st *ServiceTester) waitHealthy(ctx context.Context, serviceID string) error {
	ticker := time.NewTicker(waitPollInterval)
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

func (st *ServiceTester) Delete(ctx context.Context, serviceID string) error {
	_, err := st.client.ServiceRemove(ctx, serviceID, dock.ServiceRemoveOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("delete service %q: %w", serviceID, err)
	}

	return nil
}

func (st *ServiceTester) DeleteAndWait(ctx context.Context, serviceID string) error {
	if err := st.Delete(ctx, serviceID); err != nil {
		return err
	}

	return st.WaitRemoved(ctx, serviceID)
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

	completionService := st.isCompletionService(service.Service.Spec)
	desired := st.desiredTasks(service.Service)
	healthy := 0
	statuses := make([]string, 0, len(tasks.Items))
	for _, task := range tasks.Items {
		current := st.isCurrentTask(service.Service, task)
		statuses = append(statuses, fmt.Sprintf(
			"%s:state=%s:desired=%s:current=%t:error=%s",
			task.ID,
			task.Status.State,
			task.DesiredState,
			current,
			task.Status.Err,
		))
		if !current {
			continue
		}

		if !completionService && task.DesiredState != swarm.TaskStateRunning {
			continue
		}
		if task.Status.State == swarm.TaskStateRejected || task.Status.State == swarm.TaskStateFailed {
			return false, strings.Join(statuses, ", "), fmt.Errorf(
				"service %q task %q failed: state=%s err=%s",
				service.Service.Spec.Name,
				task.ID,
				task.Status.State,
				task.Status.Err,
			)
		}
		if completionService && task.Status.State == swarm.TaskStateComplete {
			healthy++
		}
		if !completionService && task.Status.State == swarm.TaskStateRunning {
			healthy++
		}
	}

	if healthy >= desired {
		return true, strings.Join(statuses, ", "), nil
	}

	taskStatuses := strings.Join(statuses, ", ")
	if taskStatuses == "" {
		taskStatuses = "none"
	}

	return false, fmt.Sprintf(
		"healthy tasks: %d/%d; tasks: %s",
		healthy,
		desired,
		taskStatuses,
	), nil
}

func (st *ServiceTester) WaitRemoved(ctx context.Context, serviceID string) error {
	ticker := time.NewTicker(waitPollInterval)
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

func (*ServiceTester) desiredTasks(service swarm.Service) int {
	spec := service.Spec
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
	if service.ServiceStatus != nil {
		return int(service.ServiceStatus.DesiredTasks)
	}

	return 1
}

func (*ServiceTester) isCurrentTask(service swarm.Service, task swarm.Task) bool {
	if service.JobStatus != nil {
		return task.JobIteration != nil && task.JobIteration.Index == service.JobStatus.JobIteration.Index
	}

	return reflect.DeepEqual(task.Spec, service.Spec.TaskTemplate)
}

func (*ServiceTester) isCompletionService(spec swarm.ServiceSpec) bool {
	if spec.Mode.ReplicatedJob != nil || spec.Mode.GlobalJob != nil {
		return true
	}

	return spec.TaskTemplate.RestartPolicy != nil &&
		spec.TaskTemplate.RestartPolicy.Condition == swarm.RestartPolicyConditionNone
}
