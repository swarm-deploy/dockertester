package dockertester

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
)

const serviceObservationInterval = 100 * time.Millisecond

// ServiceSnapshot is observable Swarm state. Treat snapshots as immutable.
// Tasks include terminal history, which Swarm may subsequently garbage-collect.
type ServiceSnapshot struct {
	// Service contains identity, specification, version and rollout metadata.
	Service swarm.Service
	// Tasks contains all retained tasks, sorted by ID.
	Tasks []swarm.Task
}

// Snapshot captures a service and its retained tasks. Service reads bracket the
// task read so a concurrent service update cannot silently become a baseline.
// Swarm does not provide atomic service/task reads; this is an observation, not a
// transactional snapshot.
func (st *ServiceTester) Snapshot(ctx context.Context, serviceID string) (ServiceSnapshot, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ServiceSnapshot{}, fmt.Errorf("snapshot service %q: %w", serviceID, err)
		}
		before, err := st.client.ServiceInspect(ctx, serviceID, dock.ServiceInspectOptions{})
		if err != nil {
			return ServiceSnapshot{}, fmt.Errorf("inspect service %q: %w", serviceID, err)
		}
		tasks, err := st.client.TaskList(ctx, dock.TaskListOptions{Filters: dock.Filters{}.Add("service", before.Service.ID)})
		if err != nil {
			return ServiceSnapshot{}, fmt.Errorf("list service %q tasks: %w", serviceID, err)
		}
		after, err := st.client.ServiceInspect(ctx, serviceID, dock.ServiceInspectOptions{})
		if err != nil {
			return ServiceSnapshot{}, fmt.Errorf("reinspect service %q: %w", serviceID, err)
		}
		if serviceDifference(before.Service, after.Service) == "" {
			slices.SortFunc(tasks.Items, func(a, b swarm.Task) int { return strings.Compare(a.ID, b.ID) })
			return ServiceSnapshot{Service: after.Service, Tasks: tasks.Items}, nil
		}
		if err := observeAfter(ctx, serviceObservationInterval); err != nil {
			return ServiceSnapshot{}, fmt.Errorf("snapshot service %q changed during observation: %w", serviceID, err)
		}
	}
}

// WaitRunningTasks waits for exactly expected tasks whose current and desired
// states are running. Extra running tasks (including those being shut down) and
// additional nonterminal desired-running tasks prevent success. Terminal history
// does not count. Returned task IDs are sorted. The caller controls the deadline.
func (st *ServiceTester) WaitRunningTasks(ctx context.Context, serviceID string, expected int) ([]string, error) {
	if expected < 0 {
		return nil, fmt.Errorf("wait service %q running tasks: negative expected count %d", serviceID, expected)
	}
	var last string
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("wait service %q for %d running tasks: %w; last observation: %s", serviceID, expected, err, last)
		}
		snapshot, err := st.Snapshot(ctx, serviceID)
		if err != nil {
			return nil, fmt.Errorf("wait service %q for %d running tasks: %w; last observation: %s", serviceID, expected, err, last)
		}
		ids := make([]string, 0, expected)
		running, desired := 0, 0
		for _, task := range snapshot.Tasks {
			if task.Status.State == swarm.TaskStateRunning {
				running++
			}
			if task.DesiredState == swarm.TaskStateRunning && !terminalTask(task.Status.State) {
				desired++
				if task.Status.State == swarm.TaskStateRunning {
					ids = append(ids, task.ID)
				}
			}
		}
		if len(ids) == expected && running == expected && desired == expected {
			return ids, nil
		}
		last = fmt.Sprintf("running=%d desired-active=%d tasks=[%s]", running, desired, taskSummary(snapshot.Tasks))
		if err := observeAfter(ctx, serviceObservationInterval); err != nil {
			return nil, fmt.Errorf("wait service %q for %d running tasks: %w; last observation: %s", serviceID, expected, err, last)
		}
	}
}

// WaitStable waits for a continuous quiet interval of service metadata and task
// observations, excluding terminal task garbage collection. An update or rollback
// in progress cannot settle; a paused rollout returns its snapshot and an error.
// A settled rollback or failed/completed task is not a workload health guarantee:
// callers must inspect the returned state. The caller controls the deadline.
func (st *ServiceTester) WaitStable(ctx context.Context, serviceID string, stableFor time.Duration) (ServiceSnapshot, error) {
	if stableFor <= 0 {
		return ServiceSnapshot{}, fmt.Errorf("wait service %q stable: interval must be positive", serviceID)
	}
	var last ServiceSnapshot
	quietSince := time.Now()
	first := true
	for {
		current, err := st.Snapshot(ctx, serviceID)
		if err != nil {
			return last, fmt.Errorf("wait service %q stable: %w; last version=%d update=%v", serviceID, err, last.Service.Version.Index, last.Service.UpdateStatus)
		}
		pending := false
		if status := current.Service.UpdateStatus; status != nil {
			switch status.State {
			case swarm.UpdateStateUpdating, swarm.UpdateStateRollbackStarted:
				pending = true
			case swarm.UpdateStatePaused, swarm.UpdateStateRollbackPaused:
				return current, fmt.Errorf("service %q rollout %s: %s", serviceID, status.State, status.Message)
			case swarm.UpdateStateCompleted, swarm.UpdateStateRollbackCompleted:
			default:
				return current, fmt.Errorf("service %q has unknown rollout state %q", serviceID, status.State)
			}
		}
		if first || pending || snapshotDifference(last, current) != "" {
			quietSince = time.Now()
		}
		last, first = current, false
		if !pending && time.Since(quietSince) >= stableFor {
			return current, nil
		}
		if err := observeAfter(ctx, serviceObservationInterval); err != nil {
			return last, fmt.Errorf("wait service %q stable: %w; last version=%d update=%v tasks=[%s]", serviceID, err,
				last.Service.Version.Index, last.Service.UpdateStatus, taskSummary(last.Tasks))
		}
	}
}

// WaitUnchanged observes baseline immediately and polls approximately every 100ms for the
// entire interval, failing on the first difference. It verifies ID, spec, version,
// rollout metadata, task identities and current/desired states. Terminal history may disappear or change between
// terminal states. New tasks, even terminal ones, fail.
// Like any sampled observer, it cannot see tasks created AND garbage-collected
// entirely between polls. No internal timeout is imposed on the caller's context.
func (st *ServiceTester) WaitUnchanged(ctx context.Context, baseline ServiceSnapshot, observeFor time.Duration) error {
	if observeFor <= 0 {
		return fmt.Errorf("observe unchanged service: interval must be positive")
	}
	if baseline.Service.ID == "" {
		return fmt.Errorf("observe unchanged service: baseline service ID is empty")
	}
	until := time.Now().Add(observeFor)
	for {
		current, err := st.Snapshot(ctx, baseline.Service.ID)
		if err != nil {
			return fmt.Errorf("observe unchanged service %q: %w", baseline.Service.ID, err)
		}
		if difference := snapshotDifference(baseline, current); difference != "" {
			return fmt.Errorf("service %q changed: %s", baseline.Service.ID, difference)
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			return nil
		}
		if err := observeAfter(ctx, min(serviceObservationInterval, remaining)); err != nil {
			return fmt.Errorf("observe unchanged service %q: %w", baseline.Service.ID, err)
		}
	}
}

func serviceDifference(before, after swarm.Service) string {
	switch {
	case before.ID != after.ID:
		return fmt.Sprintf("ID %q -> %q", before.ID, after.ID)
	case !reflect.DeepEqual(before.Spec, after.Spec):
		beforeSpec, afterSpec := reflect.ValueOf(before.Spec), reflect.ValueOf(after.Spec)
		for i := 0; i < beforeSpec.NumField(); i++ {
			if !reflect.DeepEqual(beforeSpec.Field(i).Interface(), afterSpec.Field(i).Interface()) {
				return "ServiceSpec." + beforeSpec.Type().Field(i).Name + " changed"
			}
		}
		return "ServiceSpec changed"
	case before.Version != after.Version:
		return fmt.Sprintf("version %d -> %d", before.Version.Index, after.Version.Index)
	case !reflect.DeepEqual(before.UpdateStatus, after.UpdateStatus):
		return fmt.Sprintf("UpdateStatus changed: %v -> %v", before.UpdateStatus, after.UpdateStatus)
	case !reflect.DeepEqual(before.JobStatus, after.JobStatus):
		return fmt.Sprintf("JobStatus changed: %v -> %v", before.JobStatus, after.JobStatus)
	default:
		return ""
	}
}

func snapshotDifference(before, after ServiceSnapshot) string {
	if difference := serviceDifference(before.Service, after.Service); difference != "" {
		return difference
	}
	known := make(map[string]swarm.Task, len(before.Tasks))
	for _, task := range before.Tasks {
		known[task.ID] = task
	}
	for _, task := range after.Tasks {
		previous, ok := known[task.ID]
		if !ok {
			return fmt.Sprintf("new task %q (current=%s desired=%s)", task.ID, task.Status.State, task.DesiredState)
		}
		if !(terminalTask(previous.Status.State) && terminalTask(task.Status.State)) &&
			(previous.Status.State != task.Status.State || previous.DesiredState != task.DesiredState) {
			return fmt.Sprintf("task %q current/desired %s/%s -> %s/%s", task.ID,
				previous.Status.State, previous.DesiredState, task.Status.State, task.DesiredState)
		}
		delete(known, task.ID)
	}
	for _, task := range before.Tasks {
		if _, missing := known[task.ID]; missing && !terminalTask(task.Status.State) {
			return fmt.Sprintf("nonterminal task %q disappeared (current=%s desired=%s)", task.ID, task.Status.State, task.DesiredState)
		}
	}
	return ""
}

func terminalTask(state swarm.TaskState) bool {
	switch state {
	case swarm.TaskStateComplete, swarm.TaskStateShutdown, swarm.TaskStateFailed, swarm.TaskStateRejected, swarm.TaskStateRemove, swarm.TaskStateOrphaned:
		return true
	default:
		return false
	}
}

func taskSummary(tasks []swarm.Task) string {
	states := make([]string, 0, len(tasks))
	for _, task := range tasks {
		states = append(states, fmt.Sprintf("%s:current=%s desired=%s err=%q", task.ID, task.Status.State, task.DesiredState, task.Status.Err))
	}
	return strings.Join(states, ", ")
}

func observeAfter(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
