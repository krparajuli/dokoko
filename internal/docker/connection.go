package docker

import (
	"context"
	"fmt"

	"dokoko.ai/dokoko/pkg/logger"
	dockerclient "github.com/docker/docker/client"
)

// Connection wraps a Docker client and owns its lifecycle.
type Connection struct {
	client *dockerclient.Client
	log    *logger.Logger
}

// New creates a Docker client and performs an initial ping to verify
// connectivity. Pass any dockerclient.Opt values to override the defaults
// (socket path, TLS, API version, etc.).
func New(ctx context.Context, log *logger.Logger, opts ...dockerclient.Opt) (*Connection, error) {
	log.LowTrace("creating docker connection")
	log.Trace("building docker client with %d custom opts", len(opts))

	defaults := []dockerclient.Opt{dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation()}
	all := append(defaults, opts...)

	log.Debug("instantiating docker client (opts=%d total)", len(all))

	c, err := dockerclient.NewClientWithOpts(all...)
	if err != nil {
		log.Error("docker client instantiation failed: %v", err)
		return nil, fmt.Errorf("docker client: %w", err)
	}

	log.Trace("client created, negotiated API version: %s", c.ClientVersion())

	conn := &Connection{client: c, log: log}

	log.Debug("pinging docker daemon to verify connectivity")

	if err := conn.Ping(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}

	log.Info("docker connection ready (api=%s)", c.ClientVersion())
	return conn, nil
}

// Ping sends a ping to the daemon and logs the response details.
// Returns an error if the daemon is unreachable.
func (c *Connection) Ping(ctx context.Context) error {
	c.log.LowTrace("pinging docker daemon")

	resp, err := c.client.Ping(ctx)
	if err != nil {
		c.log.Error("ping failed: %v", err)
		return fmt.Errorf("docker ping: %w", err)
	}

	c.log.Debug("ping response: api=%s os=%s experimental=%v builder=%s",
		resp.APIVersion, resp.OSType, resp.Experimental, resp.BuilderVersion)
	c.log.Info("docker daemon reachable (api=%s, os=%s)", resp.APIVersion, resp.OSType)

	c.log.Trace("negotiating API version from ping response")
	c.client.NegotiateAPIVersionPing(resp)
	c.log.Debug("API version negotiated: %s", c.client.ClientVersion())

	return nil
}

// Client returns the underlying Docker client for use by sub-packages.
func (c *Connection) Client() *dockerclient.Client {
	c.log.Trace("returning underlying docker client (version=%s)", c.client.ClientVersion())
	return c.client
}

// Close shuts down the HTTP transport held by the Docker client.
func (c *Connection) Close() error {
	c.log.LowTrace("closing docker connection")

	if err := c.client.Close(); err != nil {
		c.log.Error("error closing docker client: %v", err)
		return fmt.Errorf("closing docker client: %w", err)
	}

	c.log.Info("docker connection closed")
	return nil
}
