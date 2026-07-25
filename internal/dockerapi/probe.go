package dockerapi

import (
	"context"
	"fmt"

	"github.com/lev-goryachev/lctk/internal/buildinfo"
	"github.com/moby/moby/client"
)

type Status struct {
	Available  bool   `json:"available"`
	APIVersion string `json:"api_version,omitempty"`
	OSType     string `json:"os_type,omitempty"`
}

func Probe(ctx context.Context) (Status, error) {
	apiClient, err := client.New(
		client.FromEnv,
		client.WithUserAgent("lctk/"+buildinfo.Version),
	)
	if err != nil {
		return Status{}, fmt.Errorf("create Docker API client: %w", err)
	}
	defer apiClient.Close()

	result, err := apiClient.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return Status{}, fmt.Errorf(
			"Docker Desktop is unavailable; start Docker Desktop and retry: %w",
			err,
		)
	}
	return Status{
		Available:  true,
		APIVersion: result.APIVersion,
		OSType:     result.OSType,
	}, nil
}
