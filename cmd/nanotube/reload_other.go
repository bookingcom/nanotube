//go:build !linux

package main

import (
	"context"
	"fmt"
	"time"
)

func handoffService(_ context.Context, _ int, _ time.Duration) error {
	if notifySocket() != "" {
		return fmt.Errorf("systemd ownership handoff is only supported on Linux")
	}
	return nil
}
