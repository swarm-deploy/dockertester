package dockertester

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

var ErrSecretNotFound = errors.New("secret not found")

const defaultReadSecretImage = "alpine:3.20"
const readSecretTargetName = "dockertester-secret"

type SecretTester struct {
	client dock.APIClient
}

func NewSecretTester(client dock.APIClient) *SecretTester {
	return &SecretTester{client: client}
}

func SecretRef(targetName string, secretName string, secretID string) *swarm.SecretReference {
	return &swarm.SecretReference{
		File: &swarm.SecretReferenceFileTarget{
			Name: targetName,
			UID:  "0",
			GID:  "0",
			Mode: 0400,
		},
		SecretName: secretName,
		SecretID:   secretID,
	}
}

func (st *SecretTester) GetSecret(ctx context.Context, name string) (*swarm.Secret, error) {
	secrets, err := st.client.SecretList(ctx, dock.SecretListOptions{
		Filters: dock.Filters{}.Add("name", name),
	})
	if err != nil {
		return nil, fmt.Errorf("list secret %q: %w", name, err)
	}

	for _, secret := range secrets.Items {
		if secret.Spec.Name == name {
			return &secret, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, name)
}

func (st *SecretTester) RequireNotExists(ctx context.Context, t *testing.T, name string) {
	t.Helper()

	secret, err := st.GetSecret(ctx, name)
	if err != nil {
		require.ErrorIs(t, err, ErrSecretNotFound)

		return
	}

	t.Fatalf("Docker secret %q already exists with id %q", name, secret.ID)
}

func (st *SecretTester) CreateSecret(ctx context.Context, path string, value []byte) (string, error) {
	created, err := st.client.SecretCreate(ctx, dock.SecretCreateOptions{
		Spec: swarm.SecretSpec{
			Annotations: swarm.Annotations{
				Name: path,
			},
			Data: value,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create secret %q: %w", path, err)
	}

	return created.ID, nil
}

func (st *SecretTester) Delete(ctx context.Context, name string) error {
	_, err := st.client.SecretRemove(ctx, name, dock.SecretRemoveOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("delete secret %q: %w", name, err)
	}

	return nil
}

func (st *SecretTester) DeleteWithRetry(ctx context.Context, name string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		err := st.Delete(ctx, name)
		if err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("delete secret %q: %w: %v", name, ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

func (st *SecretTester) DeleteByLabels(ctx context.Context, labels map[string]string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		secrets, err := st.ListSecrets(ctx, labels)
		if err != nil {
			return err
		}
		if len(secrets) == 0 {
			return nil
		}

		var deleteErr error
		for _, secret := range secrets {
			if err = st.Delete(ctx, secret.ID); err != nil {
				deleteErr = err
			}
		}
		if deleteErr == nil {
			continue
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("delete secrets by labels %v: %w: %v", labels, ctx.Err(), deleteErr)
		case <-ticker.C:
		}
	}
}

func (st *SecretTester) ReadSecret(ctx context.Context, secret *swarm.Secret) (string, error) {
	if secret == nil {
		return "", errors.New("secret is required")
	}
	if secret.ID == "" {
		return "", errors.New("secret id is required")
	}
	if secret.Spec.Name == "" {
		return "", errors.New("secret name is required")
	}

	spec := readSecretServiceSpec(secret)
	created, err := st.client.ServiceCreate(ctx, dock.ServiceCreateOptions{Spec: spec})
	if err != nil {
		return "", fmt.Errorf("deploy read secret service %q: %w", spec.Name, err)
	}

	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = NewServiceTester(st.client).DeleteAndWait(cleanupCtx, created.ID)
	}()

	serviceTester := NewServiceTester(st.client)
	if err = serviceTester.WaitHealthy(ctx, created.ID); err != nil {
		logs, logsErr := st.readServiceLogs(ctx, created.ID)
		if logsErr != nil {
			return "", fmt.Errorf("wait read secret service %q healthy: %w; read logs: %v", spec.Name, err, logsErr)
		}

		return "", fmt.Errorf("wait read secret service %q healthy: %w: %s", spec.Name, err, logs)
	}

	value, err := st.readServiceLogs(ctx, created.ID)
	if err != nil {
		return "", fmt.Errorf("read secret service %q logs: %w", spec.Name, err)
	}

	return value, nil
}

func (st *SecretTester) AssertSecretValue(
	ctx context.Context,
	t *testing.T,
	secret *swarm.Secret,
	expectedValue string,
) {
	t.Helper()

	actual, err := st.ReadSecret(ctx, secret)
	require.NoError(t, err)
	require.Equal(t, expectedValue, actual)
}

func (st *SecretTester) ListSecrets(ctx context.Context, labels map[string]string) ([]swarm.Secret, error) {
	filters := dock.Filters{}
	for key, value := range labels {
		filters = filters.Add("label", key+"="+value)
	}

	secrets, err := st.client.SecretList(ctx, dock.SecretListOptions{
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("list secrets by labels %v: %w", labels, err)
	}

	out := make([]swarm.Secret, 0, len(secrets.Items))
	for _, secret := range secrets.Items {
		if labelsMatch(secret.Spec.Labels, labels) {
			out = append(out, secret)
		}
	}

	return out, nil
}

func (st *SecretTester) Wait(
	ctx context.Context,
	t *testing.T,
	versionID string,
	labelMatcher func(labels map[string]string) bool,
) *swarm.Secret {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		secret, err := st.findByVersion(waitCtx, versionID, labelMatcher)
		if err == nil {
			return secret
		}
		lastErr = err

		select {
		case <-waitCtx.Done():
			require.NoError(t, lastErr)
			require.NoError(t, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (st *SecretTester) findByVersion(
	ctx context.Context,
	versionID string,
	labelMatcher func(labels map[string]string) bool,
) (*swarm.Secret, error) {
	secrets, err := st.ListSecrets(ctx, map[string]string{
		"external_version_id": versionID,
	})
	if err != nil {
		return nil, err
	}

	for _, secret := range secrets {
		labels := secret.Spec.Labels
		if labelMatcher != nil && !labelMatcher(labels) {
			continue
		}

		return &secret, nil
	}

	return nil, fmt.Errorf("no secret with external_version_id=%q matching labels", versionID)
}

func labelsMatch(actual map[string]string, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}

	return true
}

func (st *SecretTester) readServiceLogs(ctx context.Context, serviceID string) (string, error) {
	logs, err := st.client.ServiceLogs(ctx, serviceID, dock.ServiceLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "all",
	})
	if err != nil {
		return "", err
	}
	defer logs.Close()

	value, err := io.ReadAll(logs)
	if err != nil {
		return "", err
	}

	return string(value), nil
}

func readSecretServiceSpec(secret *swarm.Secret) swarm.ServiceSpec {
	replicas := uint64(1)

	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{
			Name: fmt.Sprintf("dockertester-read-secret-%d", time.Now().UnixNano()),
		},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image: defaultReadSecretImage,
				Command: []string{
					"sh",
					"-ec",
				},
				Args: []string{
					`cat "$DOCKERTESTER_SECRET_FILE"`,
				},
				Env: []string{
					"DOCKERTESTER_SECRET_FILE=/run/secrets/" + readSecretTargetName,
				},
				TTY: true,
				Secrets: []*swarm.SecretReference{
					SecretRef(readSecretTargetName, secret.Spec.Name, secret.ID),
				},
			},
			RestartPolicy: &swarm.RestartPolicy{
				Condition: swarm.RestartPolicyConditionNone,
			},
		},
		Mode: swarm.ServiceMode{
			Replicated: &swarm.ReplicatedService{
				Replicas: &replicas,
			},
		},
	}
}
