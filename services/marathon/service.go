package marathon

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/influxdata/kapacitor/services/scraper"
	"github.com/prometheus/prometheus/config"
	pmarathon "github.com/prometheus/prometheus/discovery/marathon"
)

// Service is the marathon discovery service
type Service struct {
	Configs []Config
	mu      sync.Mutex

	registry scraper.Registry

	logger *log.Logger
	open   bool
}

// NewService creates a new unopened service
func NewService(c []Config, r scraper.Registry, l *log.Logger) *Service {
	return &Service{
		Configs:  c,
		registry: r,
		logger:   l,
	}
}

// Open starts the service
func (s *Service) Open() error {
	if s.open {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.open = true
	s.register()

	s.logger.Println("I! opened service")
	return s.registry.Commit()
}

// Close stops the service
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.open {
		return nil
	}

	s.open = false
	s.deregister()

	s.logger.Println("I! closed service")
	return s.registry.Commit()
}

func (s *Service) deregister() {
	// Remove all the configurations in the registry
	for _, d := range s.Configs {
		s.registry.RemoveDiscoverer(&d)
	}
}

func (s *Service) register() {
	// Add all configurations to registry
	for _, d := range s.Configs {
		if d.Enabled {
			s.registry.AddDiscoverer(&d)
		}
	}
}

// Update updates configuration while running
func (s *Service) Update(newConfigs []interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	configs := make([]Config, len(newConfigs))
	for i, c := range newConfigs {
		if config, ok := c.(Config); ok {
			configs[i] = config
		} else {
			return fmt.Errorf("unexpected config object type, got %T exp %T", c, config)
		}
	}

	s.deregister()
	s.Configs = configs
	s.register()

	return s.registry.Commit()
}

type testOptions struct {
	ID string `json:"id"`
}

// TestOptions returns an object that is in turn passed to Test.
func (s *Service) TestOptions() interface{} {
	return &testOptions{}
}

// Test a service with the provided options.
func (s *Service) Test(options interface{}) error {
	o, ok := options.(*testOptions)
	if !ok {
		return fmt.Errorf("unexpected options type %T", options)
	}

	found := -1
	for i := range s.Configs {
		if s.Configs[i].ID == o.ID && s.Configs[i].Enabled {
			found = i
		}
	}
	if found < 0 {
		return fmt.Errorf("discoverer %q is not enabled or does not exist", o.ID)
	}

	sd := s.Configs[found].PromConfig()
	discoverer, err := pmarathon.NewDiscovery(sd, scraper.NewLogger(s.logger))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan []*config.TargetGroup)
	go discoverer.Run(ctx, updates)

	select {
	case _, ok := <-updates:
		// Handle the case that a target provider exits and closes the channel
		// before the context is done.
		if !ok {
			err = fmt.Errorf("discoverer %q exited ", o.ID)
		}
		break
	case <-time.After(30 * time.Second):
		err = fmt.Errorf("timeout waiting for discoverer %q to return", o.ID)
		break
	}
	cancel()

	return err
}