package dockertester

import (
	"fmt"

	dock "github.com/moby/moby/client"
)

type Tester struct {
	client dock.APIClient

	service *ServiceTester
	network *NetworkTester
	volume  *VolumeTester
	secret  *SecretTester
	swarm   *SwarmTester
}

func NewTester() (*Tester, error) {
	client, err := dock.New(dock.FromEnv, dock.WithAPIVersionFromEnv())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	return &Tester{
		client:  client,
		service: NewServiceTester(client),
		network: NewNetworkTester(client),
		volume:  NewVolumeTester(client),
		secret:  NewSecretTester(client),
		swarm:   NewSwarmTester(client),
	}, nil
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

func (t *Tester) Close() error {
	return t.client.Close()
}
