//go:build windows

package winops

import (
	"context"
	"errors"
)

// Stubs: each is replaced by its real implementation.
var errTODO = errors.New("not implemented yet")

func checkUpdates(context.Context, logFunc) (any, error)                       { return nil, errTODO }
func upgradePackages(context.Context, map[string]string, logFunc) (any, error) { return nil, errTODO }
func servicesList(context.Context) (any, error)                                { return nil, errTODO }
func serviceAction(context.Context, map[string]string, logFunc) (any, error)   { return nil, errTODO }
func logsRead(context.Context, map[string]string) (any, error)                 { return nil, errTODO }
