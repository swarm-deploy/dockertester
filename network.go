package dockertester

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	dock "github.com/moby/moby/client"
)

type NetworkTester struct {
	client dock.APIClient
}

func NewNetworkTester(client dock.APIClient) *NetworkTester {
	return &NetworkTester{client: client}
}

func (c *NetworkTester) CreateSwarmOverlay(ctx context.Context, name string) (string, error) {
	created, err := c.client.NetworkCreate(ctx, name, dock.NetworkCreateOptions{
		Driver:     "overlay",
		Scope:      "swarm",
		Attachable: true,
	})
	if err != nil {
		return "", fmt.Errorf("create network %q: %w", name, err)
	}

	return created.ID, nil
}

func (c *NetworkTester) Delete(ctx context.Context, networkID string) error {
	_, err := c.client.NetworkRemove(ctx, networkID, dock.NetworkRemoveOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("delete network %q: %w", networkID, err)
	}
	return nil
}
