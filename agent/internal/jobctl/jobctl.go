// Package jobctl holds the job time limits and error wording shared by the
// Linux and Windows op runners.
package jobctl

import (
	"context"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/jacksonm36/servermgr/gen/agent/v1"
)

// Timeout is how long a job may run. Most ops are quick; installs,
// upgrades, backups and replication get hours.
func Timeout(op agentv1.Op) time.Duration {
	switch op {
	case agentv1.Op_OP_ZFS_REPL_RUN:
		return 24 * time.Hour
	case agentv1.Op_OP_PACKAGES_UPGRADE, agentv1.Op_OP_BACKUP_CREATE, agentv1.Op_OP_BACKUP_RESTORE,
		agentv1.Op_OP_WIN_APPS_INSTALL:
		return 3 * time.Hour
	case agentv1.Op_OP_WEB_INSTALL, agentv1.Op_OP_PHP_INSTALL, agentv1.Op_OP_DB_INSTALL, agentv1.Op_OP_DOCKER_INSTALL,
		agentv1.Op_OP_COMPOSE_SAVE, agentv1.Op_OP_COMPOSE_ACTION, agentv1.Op_OP_APP_DEPLOY, agentv1.Op_OP_APP_UPDATE,
		agentv1.Op_OP_CROWDSEC_INSTALL, agentv1.Op_OP_FAIL2BAN_INSTALL, agentv1.Op_OP_FIREWALL_INSTALL,
		agentv1.Op_OP_ZFS_POOL_CREATE, agentv1.Op_OP_ZFS_SNAPSHOT, agentv1.Op_OP_ZFS_DATASET, agentv1.Op_OP_ZFS_POOL,
		agentv1.Op_OP_DOCKER_PRUNE, agentv1.Op_OP_ANSIBLE_PREPARE, agentv1.Op_OP_WIN_APP_UNINSTALL:
		return time.Hour
	}
	return 15 * time.Minute
}

// Error explains why a job's context ended, if it did.
func Error(ctx context.Context, err error, limit time.Duration) error {
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.DeadlineExceeded) && !errors.Is(cause, context.Canceled) {
		return fmt.Errorf("%w (%v)", cause, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s: %w", limit, err)
	}
	return err
}
