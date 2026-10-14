package dockertester

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestServiceHealthyIgnoresHistoricalFailedTasks(t *testing.T) {
	spec := replicatedSpec(1, 2)
	oldSpec := spec.TaskTemplate
	oldSpec.ForceUpdate = 1
	client := serviceWithTasks(spec, []swarm.Task{
		{
			ID:           "old-failed",
			Spec:         oldSpec,
			DesiredState: swarm.TaskStateShutdown,
			Status:       swarm.TaskStatus{State: swarm.TaskStateFailed, Err: "old failure"},
		},
		currentTask("current", spec.TaskTemplate, swarm.TaskStateRunning),
	})

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.True(t, healthy)
	require.Contains(t, status, "old-failed")
	require.Contains(t, status, "current=false")
}

func TestServiceHealthyDuringRollingUpdate(t *testing.T) {
	spec := replicatedSpec(2, 2)
	oldSpec := spec.TaskTemplate
	oldSpec.ForceUpdate = 1
	updatingTasks := []swarm.Task{
		currentTask("old-one", oldSpec, swarm.TaskStateRunning),
		currentTask("old-two", oldSpec, swarm.TaskStateRunning),
		currentTask("new-one", spec.TaskTemplate, swarm.TaskStateRunning),
	}
	client := serviceWithTasks(spec, updatingTasks)

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.False(t, healthy)
	require.Contains(t, status, "healthy tasks: 1/2")

	calls := 0
	client.taskList = func(context.Context, dock.TaskListOptions) (dock.TaskListResult, error) {
		calls++
		if calls == 1 {
			return dock.TaskListResult{Items: updatingTasks}, nil
		}

		return dock.TaskListResult{Items: []swarm.Task{
			currentTask("new-one", spec.TaskTemplate, swarm.TaskStateRunning),
			currentTask("new-two", spec.TaskTemplate, swarm.TaskStateRunning),
		}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, NewServiceTester(client).WaitHealthy(ctx, "service"))
	require.GreaterOrEqual(t, calls, 2)
}

func TestServiceHealthyVerifiesReplicaCount(t *testing.T) {
	spec := replicatedSpec(3, 1)
	client := serviceWithTasks(spec, []swarm.Task{
		currentTask("one", spec.TaskTemplate, swarm.TaskStateRunning),
		currentTask("two", spec.TaskTemplate, swarm.TaskStateRunning),
	})

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.False(t, healthy)
	require.Contains(t, status, "healthy tasks: 2/3")
}

func TestServiceHealthyReplicatedJobUsesCurrentIteration(t *testing.T) {
	spec := swarm.ServiceSpec{
		TaskTemplate: swarm.TaskSpec{ForceUpdate: 2},
		Mode: swarm.ServiceMode{ReplicatedJob: &swarm.ReplicatedJob{
			MaxConcurrent:    uint64Ptr(1),
			TotalCompletions: uint64Ptr(2),
		}},
	}
	service := swarm.Service{
		Spec:      spec,
		JobStatus: &swarm.JobStatus{JobIteration: swarm.Version{Index: 2}},
	}
	tasks := []swarm.Task{
		jobTask("old-failed", spec.TaskTemplate, 1, swarm.TaskStateFailed),
		jobTask("complete-one", spec.TaskTemplate, 2, swarm.TaskStateComplete),
		jobTask("complete-two", spec.TaskTemplate, 2, swarm.TaskStateComplete),
	}
	client := serviceWithTasks(spec, tasks)
	client.serviceInspect = func(
		context.Context,
		string,
		dock.ServiceInspectOptions,
	) (dock.ServiceInspectResult, error) {
		return dock.ServiceInspectResult{Service: service}, nil
	}

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "job")
	require.NoError(t, err)
	require.True(t, healthy)
	require.Contains(t, status, "old-failed")
	require.Contains(t, status, "current=false")
}

func TestServiceWaitHealthyTimeout(t *testing.T) {
	spec := replicatedSpec(1, 1)
	client := serviceWithTasks(spec, nil)

	err := NewServiceTester(client).WaitHealthyTimeout(context.Background(), "service", time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "healthy tasks: 0/1")
}

func replicatedSpec(replicas, forceUpdate uint64) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		TaskTemplate: swarm.TaskSpec{ForceUpdate: forceUpdate},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{
			Replicas: uint64Ptr(replicas),
		}},
	}
}

func currentTask(id string, spec swarm.TaskSpec, state swarm.TaskState) swarm.Task {
	return swarm.Task{
		ID:           id,
		Spec:         spec,
		DesiredState: swarm.TaskStateRunning,
		Status:       swarm.TaskStatus{State: state},
	}
}

func jobTask(id string, spec swarm.TaskSpec, iteration uint64, state swarm.TaskState) swarm.Task {
	return swarm.Task{
		ID:           id,
		Spec:         spec,
		JobIteration: &swarm.Version{Index: iteration},
		DesiredState: state,
		Status:       swarm.TaskStatus{State: state},
	}
}
