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

func TestServiceHealthyAcceptsNormalizedTaskSpec(t *testing.T) {
	createdAt := time.Now()
	spec := replicatedSpec(1, 0)
	spec.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{Image: "nginx:latest"}
	normalizedSpec := spec.TaskTemplate
	normalizedSpec.ContainerSpec = &swarm.ContainerSpec{Image: "nginx:latest@sha256:digest"}
	client := serviceWithTasks(spec, []swarm.Task{
		serviceTask("current", 1, createdAt, normalizedSpec, swarm.TaskStateRunning),
	})

	healthy, _, err := NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.True(t, healthy)
}

func TestServiceHealthyDuringRollingUpdate(t *testing.T) {
	startedAt := time.Now()
	spec := replicatedSpec(2, 2)
	service := swarm.Service{
		Spec: spec,
		UpdateStatus: &swarm.UpdateStatus{
			State:     swarm.UpdateStateUpdating,
			StartedAt: &startedAt,
			Message:   "update in progress",
		},
	}
	oldSpec := spec.TaskTemplate
	oldSpec.ForceUpdate = 1
	tasks := []swarm.Task{
		serviceTask("old-one", 1, startedAt.Add(-time.Minute), oldSpec, swarm.TaskStateRunning),
		serviceTask("old-two", 2, startedAt.Add(-time.Minute), oldSpec, swarm.TaskStateRunning),
		serviceTask("new-one", 1, startedAt.Add(time.Second), spec.TaskTemplate, swarm.TaskStateRunning),
	}
	client := serviceClient(service, tasks)

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.False(t, healthy)
	require.Contains(t, status, "healthy tasks: 1/2")
	require.Contains(t, status, "old-one:state=running:desired=running:current=false")

	service.UpdateStatus.State = swarm.UpdateStateCompleted
	service.UpdateStatus.Message = "update completed"
	tasks = append(tasks,
		serviceTask("new-two", 2, startedAt.Add(2*time.Second), spec.TaskTemplate, swarm.TaskStateRunning),
	)
	client = serviceClient(service, tasks)
	healthy, _, err = NewServiceTester(client).Healthy(context.Background(), "service")
	require.NoError(t, err)
	require.True(t, healthy)
}

func TestServiceWaitHealthyRecoversFromTransientTaskFailure(t *testing.T) {
	for _, failedState := range []swarm.TaskState{swarm.TaskStateFailed, swarm.TaskStateRejected} {
		t.Run(string(failedState), func(t *testing.T) {
			createdAt := time.Now()
			spec := replicatedSpec(1, 1)
			failed := serviceTask("failed", 1, createdAt, spec.TaskTemplate, failedState)
			failed.Status.Err = "temporary failure"
			replacement := serviceTask(
				"replacement",
				1,
				createdAt.Add(time.Second),
				spec.TaskTemplate,
				swarm.TaskStateRunning,
			)
			client := serviceWithTasks(spec, nil)
			calls := 0
			client.taskList = func(context.Context, dock.TaskListOptions) (dock.TaskListResult, error) {
				calls++
				if calls == 1 {
					return dock.TaskListResult{Items: []swarm.Task{failed}}, nil
				}

				return dock.TaskListResult{Items: []swarm.Task{failed, replacement}}, nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			require.NoError(t, NewServiceTester(client).WaitHealthy(ctx, "service"))
			require.GreaterOrEqual(t, calls, 2)
		})
	}
}

func TestServiceWaitHealthyPersistentFailureTimesOutWithDiagnostics(t *testing.T) {
	createdAt := time.Now()
	spec := replicatedSpec(1, 1)
	failed := serviceTask("failed", 1, createdAt, spec.TaskTemplate, swarm.TaskStateFailed)
	failed.Status.Err = "image pull failed"
	oldHealthy := serviceTask(
		"old-healthy",
		1,
		createdAt.Add(-time.Second),
		spec.TaskTemplate,
		swarm.TaskStateRunning,
	)
	client := serviceWithTasks(spec, []swarm.Task{oldHealthy, failed})

	err := NewServiceTester(client).WaitHealthyTimeout(context.Background(), "service", time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "state=failed")
	require.ErrorContains(t, err, "image pull failed")
}

func TestServiceWaitHealthyHonorsContextCancellation(t *testing.T) {
	createdAt := time.Now()
	spec := replicatedSpec(1, 1)
	client := serviceWithTasks(spec, []swarm.Task{
		serviceTask("pending", 1, createdAt, spec.TaskTemplate, swarm.TaskStatePending),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewServiceTester(client).WaitHealthy(ctx, "service")
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "state=pending")
}

func TestServiceWaitHealthyReturnsDockerAPIErrors(t *testing.T) {
	dockerErr := errors.New("daemon unavailable")
	client := serviceWithTasks(replicatedSpec(1, 1), nil)
	client.taskList = func(context.Context, dock.TaskListOptions) (dock.TaskListResult, error) {
		return dock.TaskListResult{}, dockerErr
	}

	err := NewServiceTester(client).WaitHealthy(context.Background(), "service")
	require.ErrorIs(t, err, dockerErr)
}

func TestServiceHealthyVerifiesReplicaCount(t *testing.T) {
	createdAt := time.Now()
	spec := replicatedSpec(3, 1)
	client := serviceWithTasks(spec, []swarm.Task{
		serviceTask("one", 1, createdAt, spec.TaskTemplate, swarm.TaskStateRunning),
		serviceTask("two", 2, createdAt, spec.TaskTemplate, swarm.TaskStateRunning),
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
	client := serviceClient(service, tasks)

	healthy, status, err := NewServiceTester(client).Healthy(context.Background(), "job")
	require.NoError(t, err)
	require.True(t, healthy)
	require.Contains(t, status, "old-failed")
	require.Contains(t, status, "current=false")
}

func replicatedSpec(replicas, forceUpdate uint64) swarm.ServiceSpec {
	return swarm.ServiceSpec{
		TaskTemplate: swarm.TaskSpec{ForceUpdate: forceUpdate},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{
			Replicas: uint64Ptr(replicas),
		}},
	}
}

func serviceTask(
	id string,
	slot int,
	createdAt time.Time,
	spec swarm.TaskSpec,
	state swarm.TaskState,
) swarm.Task {
	return swarm.Task{
		ID:           id,
		Meta:         swarm.Meta{CreatedAt: createdAt},
		Spec:         spec,
		Slot:         slot,
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
