package dockertester

import (
	"fmt"
	"testing"

	dock "github.com/moby/moby/client"
)

type Tester struct {
	client dock.APIClient

	service *ServiceTester
	network *NetworkTester
	volume  *VolumeTester
	secret  *SecretTester
	swarm   *SwarmTester
	stack   *StackTester
}

func NewTester() (*Tester, error) {
	client, err := dock.New(dock.FromEnv, dock.WithAPIVersionFromEnv())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	return newTester(client), nil
}

func newTester(client dock.APIClient) *Tester {
	return &Tester{
		client:  client,
		service: NewServiceTester(client),
		network: NewNetworkTester(client),
		volume:  NewVolumeTester(client),
		secret:  NewSecretTester(client),
		swarm:   NewSwarmTester(client),
		stack:   NewStackTester(client),
	}
}

func MustNewTester(t *testing.T) *Tester {
	t.Helper()

	tester, err := NewTester()
	if err != nil {
		t.Fatalf("create docker tester: %v", err)
	}
	t.Cleanup(func() {
		if err := tester.Close(); err != nil {
			t.Errorf("close docker tester: %v", err)
		}
	})

	return tester
}

func (t *Tester) Service() *ServiceTester {
	return t.service
}

func (t *Tester) Network() *NetworkTester {
	return t.network
}

func (t *Tester) Volume() *VolumeTester {
	return t.volume
}

func (t *Tester) Secret() *SecretTester {
	return t.secret
}

func (t *Tester) Swarm() *SwarmTester {
	return t.swarm
}

func (t *Tester) Stack() *StackTester {
	return t.stack
}

func (t *Tester) Close() error {
	return t.client.Close()
}
