package dockertester

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	dock "github.com/moby/moby/client"
)

type VolumeTester struct {
	client dock.APIClient
}

func NewVolumeTester(client dock.APIClient) *VolumeTester {
	return &VolumeTester{client: client}
}

func (vt *VolumeTester) CreateLocal(ctx context.Context, name string) error {
	_, err := vt.client.VolumeCreate(ctx, dock.VolumeCreateOptions{
		Name:   name,
		Driver: "local",
	})
	if err != nil {
		return fmt.Errorf("create volume %q: %w", name, err)
	}

	return nil
}

func (vt *VolumeTester) Delete(ctx context.Context, name string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		_, err := vt.client.VolumeRemove(ctx, name, dock.VolumeRemoveOptions{Force: true})
		if err == nil || errdefs.IsNotFound(err) {
			return nil
		}
		if !errdefs.IsConflict(err) && !strings.Contains(err.Error(), "volume is in use") {
			return fmt.Errorf("delete volume %q: %w", name, err)
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return fmt.Errorf("delete volume %q: %w: %v", name, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}
