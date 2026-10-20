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
func filesList(map[string]string) (any, error)                                 { return nil, errTODO }
func filesRead(map[string]string) (any, error)                                 { return nil, errTODO }
func filesWrite(map[string]string, logFunc) (any, error)                       { return nil, errTODO }
func filesMkdir(map[string]string, logFunc) (any, error)                       { return nil, errTODO }
func filesDelete(map[string]string, logFunc) (any, error)                      { return nil, errTODO }
func filesRename(map[string]string, logFunc) (any, error)                      { return nil, errTODO }
func firewallStatus(context.Context) (any, error)                              { return nil, errTODO }
func firewallEnable(context.Context, logFunc) (any, error)                     { return nil, errTODO }
func firewallDisable(context.Context, logFunc) (any, error)                    { return nil, errTODO }
func firewallRuleAdd(context.Context, map[string]string, logFunc) (any, error) { return nil, errTODO }
func firewallRuleDelete(context.Context, map[string]string, logFunc) (any, error) {
	return nil, errTODO
}
func winAppsList(context.Context) (any, error)                                 { return nil, errTODO }
func winAppsInstall(context.Context, map[string]string, logFunc) (any, error)  { return nil, errTODO }
func winAppUninstall(context.Context, map[string]string, logFunc) (any, error) { return nil, errTODO }
func selfUpdate(context.Context, map[string]string, logFunc) (any, error)      { return nil, errTODO }
