package dockertester

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Use the real SDK against controlled Engine HTTP responses. Ordinary tests do
// not depend on a Docker daemon or the scheduler's timing.
func observationTester(t *testing.T, observations ...ServiceSnapshot) *ServiceTester {
	t.Helper()
	var mu sync.Mutex
	index, inspections := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		current := observations[min(index, len(observations)-1)]
		var value any
		switch {
		case strings.Contains(r.URL.Path, "/services/"):
			value = current.Service
			inspections++
			if inspections%2 == 0 {
				index++
			}
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			assert.JSONEq(t, `{"service":{"`+current.Service.ID+`":true}}`, r.URL.Query().Get("filters"))
			value = current.Tasks
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(value))
	}))
	t.Cleanup(server.Close)
	cli, err := dock.New(dock.WithHost(server.URL), dock.WithAPIVersion("1.55"), dock.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cli.Close()) })
	return NewServiceTester(cli)
}

func observation() ServiceSnapshot {
	return ServiceSnapshot{
		Service: swarm.Service{ID: "service", Meta: swarm.Meta{Version: swarm.Version{Index: 12}},
			Spec: swarm.ServiceSpec{Annotations: swarm.Annotations{Name: "test"}}},
		Tasks: []swarm.Task{observationTask("running", swarm.TaskStateRunning, swarm.TaskStateRunning)},
	}
}

func observationTask(id string, current, desired swarm.TaskState) swarm.Task {
	return swarm.Task{ID: id, ServiceID: "service", Status: swarm.TaskStatus{State: current}, DesiredState: desired}
}

func TestWaitRunningTasks(t *testing.T) {
	for _, test := range []struct {
		name  string
		tasks []swarm.Task
		count int
		want  []string
	}{
		{"running sorted", []swarm.Task{observationTask("z", swarm.TaskStateRunning, swarm.TaskStateRunning), observationTask("a", swarm.TaskStateRunning, swarm.TaskStateRunning)}, 2, []string{"a", "z"}},
		{"terminal history ignored", []swarm.Task{observationTask("done", swarm.TaskStateComplete, swarm.TaskStateRunning), observationTask("failed", swarm.TaskStateFailed, swarm.TaskStateShutdown)}, 0, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := observation()
			snapshot.Tasks = test.tasks
			ids, err := observationTester(t, snapshot).WaitRunningTasks(t.Context(), "service", test.count)
			require.NoError(t, err)
			assert.Equal(t, test.want, ids)
		})
	}
}

func TestWaitRunningTasksRejectsUnsettledCounts(t *testing.T) {
	for _, test := range []struct {
		name  string
		tasks []swarm.Task
	}{
		{"missing", nil},
		{"extra running", []swarm.Task{observationTask("a", swarm.TaskStateRunning, swarm.TaskStateRunning), observationTask("b", swarm.TaskStateRunning, swarm.TaskStateRunning)}},
		{"old task still stopping", []swarm.Task{observationTask("a", swarm.TaskStateRunning, swarm.TaskStateRunning), observationTask("old", swarm.TaskStateRunning, swarm.TaskStateShutdown)}},
		{"additional pending", []swarm.Task{observationTask("a", swarm.TaskStateRunning, swarm.TaskStateRunning), observationTask("next", swarm.TaskStatePending, swarm.TaskStateRunning)}},
		{"only running is stopping", []swarm.Task{observationTask("old", swarm.TaskStateRunning, swarm.TaskStateShutdown)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := observation()
			snapshot.Tasks = test.tasks
			ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
			defer cancel()
			_, err := observationTester(t, snapshot).WaitRunningTasks(ctx, "service", 1)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.ErrorContains(t, err, "running=")
			assert.ErrorContains(t, err, "desired-active=")
		})
	}
}

func TestWaitRunningTasksEventually(t *testing.T) {
	before, after := observation(), observation()
	before.Tasks[0].Status.State = swarm.TaskStateStarting
	ids, err := observationTester(t, before, after).WaitRunningTasks(t.Context(), "service", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"running"}, ids)
}

func TestWaitUnchangedDifferences(t *testing.T) {
	for _, test := range []struct {
		name    string
		change  func(*ServiceSnapshot)
		message string
	}{
		{"identity", func(s *ServiceSnapshot) { s.Service.ID = "other" }, "ID"},
		{"version", func(s *ServiceSnapshot) { s.Service.Version.Index++ }, "version 12 -> 13"},
		{"spec", func(s *ServiceSnapshot) { s.Service.Spec.TaskTemplate.ForceUpdate++ }, "ServiceSpec"},
		{"replacement", func(s *ServiceSnapshot) { s.Tasks[0].ID = "replacement" }, "new task \"replacement\""},
		{"delayed terminal task creation", func(s *ServiceSnapshot) {
			s.Tasks = append(s.Tasks, observationTask("new", swarm.TaskStateComplete, swarm.TaskStateShutdown))
		}, "new task \"new\""},
		{"running disappears", func(s *ServiceSnapshot) { s.Tasks = nil }, "nonterminal task \"running\" disappeared"},
		{"running completes", func(s *ServiceSnapshot) { s.Tasks[0].Status.State = swarm.TaskStateComplete }, "running/running -> complete/running"},
		{"desired shutdown", func(s *ServiceSnapshot) { s.Tasks[0].DesiredState = swarm.TaskStateShutdown }, "running/running -> running/shutdown"},
		{"rollout metadata", func(s *ServiceSnapshot) {
			s.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateCompleted}
		}, "UpdateStatus"},
		{"job metadata", func(s *ServiceSnapshot) {
			s.Service.JobStatus = &swarm.JobStatus{JobIteration: swarm.Version{Index: 2}}
		}, "JobStatus"},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline, changed := observation(), observation()
			test.change(&changed)
			// Two unchanged observations ensure delayed changes are detected.
			st := observationTester(t, baseline, baseline, changed)
			err := st.WaitUnchanged(t.Context(), baseline, 250*time.Millisecond)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestWaitUnchangedAllowsTerminalGarbageCollection(t *testing.T) {
	baseline, after := observation(), observation()
	baseline.Tasks = append(baseline.Tasks, observationTask("old", swarm.TaskStateShutdown, swarm.TaskStateShutdown))
	st := observationTester(t, baseline, after)
	started := time.Now()
	require.NoError(t, st.WaitUnchanged(t.Context(), baseline, 220*time.Millisecond))
	assert.GreaterOrEqual(t, time.Since(started), 220*time.Millisecond, "must observe the full interval")
}

func TestWaitStableAsynchronousUpdates(t *testing.T) {
	first, updating, completed, metadata := observation(), observation(), observation(), observation()
	updating.Service.Version.Index++
	updating.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateUpdating}
	completed.Service.Version = updating.Service.Version
	completed.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateCompleted}
	metadata.Service.Version = completed.Service.Version
	metadata.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateCompleted, Message: "final metadata"}
	st := observationTester(t, first, updating, completed, metadata)
	started := time.Now()
	snapshot, err := st.WaitStable(t.Context(), "service", 150*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "final metadata", snapshot.Service.UpdateStatus.Message)
	assert.GreaterOrEqual(t, time.Since(started), 450*time.Millisecond, "quiet interval resets even without a version change")
}

func TestWaitStableTerminalGarbageCollectionDoesNotReset(t *testing.T) {
	before, after := observation(), observation()
	before.Tasks = append(before.Tasks, observationTask("old", swarm.TaskStateFailed, swarm.TaskStateShutdown))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := observationTester(t, before, before, after).WaitStable(ctx, "service", 150*time.Millisecond)
	require.NoError(t, err)
}

func TestWaitStablePausedAndRollback(t *testing.T) {
	for _, state := range []swarm.UpdateState{swarm.UpdateStatePaused, swarm.UpdateStateRollbackPaused} {
		t.Run(string(state), func(t *testing.T) {
			before := observation()
			before.Service.UpdateStatus = &swarm.UpdateStatus{State: state, Message: "failure"}
			snapshot, err := observationTester(t, before).WaitStable(t.Context(), "service", time.Millisecond)
			require.ErrorContains(t, err, string(state))
			assert.Equal(t, state, snapshot.Service.UpdateStatus.State)
		})
	}
	before, after := observation(), observation()
	before.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateRollbackStarted}
	after.Service.UpdateStatus = &swarm.UpdateStatus{State: swarm.UpdateStateRollbackCompleted}
	snapshot, err := observationTester(t, before, after).WaitStable(t.Context(), "service", time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, swarm.UpdateStateRollbackCompleted, snapshot.Service.UpdateStatus.State)
}

func TestObservationCancellationAndTimeout(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		for _, method := range []string{"snapshot", "running", "stable", "unchanged"} {
			t.Run(method+map[bool]string{true: "/canceled", false: "/timeout"}[canceled], func(t *testing.T) {
				before := observation()
				before.Tasks = nil
				st := observationTester(t, before)
				ctx, cancel := context.WithCancel(t.Context())
				expected := error(context.Canceled)
				if canceled || method == "snapshot" {
					cancel()
				} else {
					cancel()
					ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
					expected = context.DeadlineExceeded
				}
				defer cancel()
				var err error
				switch method {
				case "snapshot":
					_, err = st.Snapshot(ctx, "service")
				case "running":
					_, err = st.WaitRunningTasks(ctx, "service", 1)
				case "stable":
					_, err = st.WaitStable(ctx, "service", time.Second)
				case "unchanged":
					err = st.WaitUnchanged(ctx, before, time.Second)
				}
				require.True(t, errors.Is(err, expected), "%v should wrap %v", err, expected)
			})
		}
	}
}

func TestObservationInvalidArguments(t *testing.T) {
	st := observationTester(t, observation())
	_, err := st.WaitRunningTasks(t.Context(), "service", -1)
	require.ErrorContains(t, err, "negative")
	_, err = st.WaitStable(t.Context(), "service", 0)
	require.ErrorContains(t, err, "positive")
	require.ErrorContains(t, st.WaitUnchanged(t.Context(), observation(), 0), "positive")
	require.ErrorContains(t, st.WaitUnchanged(t.Context(), ServiceSnapshot{}, time.Second), "ID is empty")
}

func TestSnapshotRetriesConcurrentServiceUpdate(t *testing.T) {
	before, after := observation(), observation()
	after.Service.Version.Index++
	var mu sync.Mutex
	inspections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/services/") {
			inspections++
			service := after.Service
			if inspections == 1 {
				service = before.Service
			}
			assert.NoError(t, json.NewEncoder(w).Encode(service))
		} else {
			assert.NoError(t, json.NewEncoder(w).Encode(after.Tasks))
		}
	}))
	t.Cleanup(server.Close)
	cli, err := dock.New(dock.WithHost(server.URL), dock.WithAPIVersion("1.55"), dock.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cli.Close()) })
	snapshot, err := NewServiceTester(cli).Snapshot(t.Context(), "service")
	require.NoError(t, err)
	assert.Equal(t, after.Service.Version, snapshot.Service.Version)
	mu.Lock()
	assert.Equal(t, 4, inspections, "must retry after an inconsistent service/task observation")
	mu.Unlock()
}

func TestSnapshotRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(server.Close)
	cli, err := dock.New(dock.WithHost(server.URL), dock.WithAPIVersion("1.55"), dock.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cli.Close()) })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = NewServiceTester(cli).Snapshot(ctx, "service")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitUnchangedAllowsTerminalHistoryRemovalState(t *testing.T) {
	baseline, after := observation(), observation()
	baseline.Tasks = append(baseline.Tasks, observationTask("old", swarm.TaskStateComplete, swarm.TaskStateRunning))
	after.Tasks = append(after.Tasks, observationTask("old", swarm.TaskStateRemove, swarm.TaskStateRemove))
	require.NoError(t, observationTester(t, after).WaitUnchanged(t.Context(), baseline, time.Millisecond))
}

func TestWaitUnchangedRejectsTerminalTaskResurrection(t *testing.T) {
	baseline, after := observation(), observation()
	baseline.Tasks[0].Status.State = swarm.TaskStateComplete
	err := observationTester(t, after).WaitUnchanged(t.Context(), baseline, time.Millisecond)
	require.ErrorContains(t, err, "complete/running -> running/running")
}
