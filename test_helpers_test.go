package dockertester

import (
	"context"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
)

type fakeDockerClient struct {
	dock.APIClient

	serviceList    func(context.Context, dock.ServiceListOptions) (dock.ServiceListResult, error)
	serviceInspect func(context.Context, string, dock.ServiceInspectOptions) (dock.ServiceInspectResult, error)
	taskList       func(context.Context, dock.TaskListOptions) (dock.TaskListResult, error)
}

func (f *fakeDockerClient) ServiceList(
	ctx context.Context,
	options dock.ServiceListOptions,
) (dock.ServiceListResult, error) {
	return f.serviceList(ctx, options)
}

func (f *fakeDockerClient) ServiceInspect(
	ctx context.Context,
	serviceID string,
	options dock.ServiceInspectOptions,
) (dock.ServiceInspectResult, error) {
	return f.serviceInspect(ctx, serviceID, options)
}

func (f *fakeDockerClient) TaskList(
	ctx context.Context,
	options dock.TaskListOptions,
) (dock.TaskListResult, error) {
	return f.taskList(ctx, options)
}

func serviceWithTasks(spec swarm.ServiceSpec, tasks []swarm.Task) *fakeDockerClient {
	return &fakeDockerClient{
		serviceInspect: func(
			context.Context,
			string,
			dock.ServiceInspectOptions,
		) (dock.ServiceInspectResult, error) {
			return dock.ServiceInspectResult{Service: swarm.Service{Spec: spec}}, nil
		},
		taskList: func(context.Context, dock.TaskListOptions) (dock.TaskListResult, error) {
			return dock.TaskListResult{Items: tasks}, nil
		},
	}
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}
