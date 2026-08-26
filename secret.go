package dockertester

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	dock "github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

var ErrSecretNotFound = errors.New("secret not found")

type SecretTester struct {
	client dock.APIClient
}

func NewSecretTester(client dock.APIClient) *SecretTester {
	return &SecretTester{client: client}
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

func (st *SecretTester) ListSecrets(ctx context.Context, logicalPath string) ([]swarm.Secret, error) {
	secrets, err := st.client.SecretList(ctx, dock.SecretListOptions{
		Filters: dock.Filters{}.Add("label", "logical_path="+logicalPath),
	})
	if err != nil {
		return nil, fmt.Errorf("list secrets by logical_path %q: %w", logicalPath, err)
	}

	out := make([]swarm.Secret, 0, len(secrets.Items))
	for _, secret := range secrets.Items {
		if secret.Spec.Labels["logical_path"] == logicalPath {
			out = append(out, secret)
		}
	}

	return out, nil
}

func (st *SecretTester) WaitSecretVersion(
	ctx context.Context,
	t *testing.T,
	secretName string,
	versionID string,
) {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		secrets, err := st.ListSecrets(waitCtx, secretName)
		if err == nil {
			for _, secret := range secrets {
				if secret.Spec.Labels["external_version_id"] == versionID {
					return
				}
			}

			lastErr = fmt.Errorf(
				"no secret with logical_path=%q and external_version_id=%q",
				secretName,
				versionID,
			)
		} else {
			lastErr = err
		}

		select {
		case <-waitCtx.Done():
			require.NoError(t, lastErr)
			require.NoError(t, waitCtx.Err())
		case <-ticker.C:
		}
	}
}
