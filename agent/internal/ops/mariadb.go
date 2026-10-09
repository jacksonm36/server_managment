package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var (
	dbIdent    = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	dbUserName = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	dbHostRe   = regexp.MustCompile(`^(localhost|%|[0-9a-fA-F.:%/]{1,64})$`)
)

var systemDBs = map[string]bool{"mysql": true, "information_schema": true, "performance_schema": true, "sys": true}

// sqlString quotes s as a MariaDB string literal.
func sqlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\x1a", `\Z`)
	return "'" + r.Replace(s) + "'"
}

// mariadb runs SQL as root over the unix socket. SQL goes through stdin so
// passwords never appear in the process list.
func mariadb(ctx context.Context, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "mariadb", "--batch", "--skip-column-names", "--protocol=socket", "-u", "root")
	cmd.Stdin = strings.NewReader(sql)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mariadb: %s", strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

func rows(out string) [][]string {
	var r [][]string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			r = append(r, strings.Split(l, "\t"))
		}
	}
	return r
}

type dbInfo struct {
	Name  string   `json:"name"`
	Size  int64    `json:"size"`
	Users []string `json:"users"`
}

type dbUser struct {
	User      string   `json:"user"`
	Host      string   `json:"host"`
	Databases []string `json:"databases"`
}

func dbStatus(ctx context.Context) (any, error) {
	if _, err := exec.LookPath("mariadb"); err != nil {
		return map[string]any{"installed": false}, nil
	}
	// Probe with a query rather than systemd so it also works for non-systemd setups.
	_, pingErr := mariadb(ctx, "SELECT 1;")
	active := pingErr == nil
	res := map[string]any{"installed": true, "active": active, "databases": []dbInfo{}, "users": []dbUser{}}
	if !active {
		return res, nil
	}
	out, err := mariadb(ctx, `SELECT VERSION();
SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0)
  FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name
  GROUP BY s.schema_name ORDER BY s.schema_name;
SELECT '--';
SELECT User, Host FROM mysql.user WHERE User NOT IN ('root', 'mysql', 'mariadb.sys', '') ORDER BY User, Host;
SELECT '--';
SELECT Db, User, Host FROM mysql.db;`)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(out, "--\n")
	if len(parts) != 3 {
		return nil, errors.New("unexpected mariadb output")
	}
	lines := rows(parts[0])
	if len(lines) > 0 {
		res["version"] = lines[0][0]
		lines = lines[1:]
	}
	grants := rows(parts[2])
	dbs := []dbInfo{}
	for _, l := range lines {
		if len(l) < 2 || systemDBs[l[0]] {
			continue
		}
		size, _ := strconv.ParseInt(l[1], 10, 64)
		d := dbInfo{Name: l[0], Size: size, Users: []string{}}
		for _, g := range grants {
			if len(g) == 3 && strings.ReplaceAll(g[0], `\_`, "_") == d.Name {
				d.Users = append(d.Users, g[1]+"@"+g[2])
			}
		}
		dbs = append(dbs, d)
	}
	users := []dbUser{}
	for _, l := range rows(parts[1]) {
		if len(l) < 2 {
			continue
		}
		u := dbUser{User: l[0], Host: l[1], Databases: []string{}}
		for _, g := range grants {
			if len(g) == 3 && g[1] == u.User && g[2] == u.Host {
				u.Databases = append(u.Databases, strings.ReplaceAll(g[0], `\_`, "_"))
			}
		}
		users = append(users, u)
	}
	res["databases"], res["users"] = dbs, users
	return res, nil
}

func dbInstall(ctx context.Context, logf func(string, ...any)) (any, error) {
	if err := aptInstall(ctx, logf, "mariadb-server", "mariadb-client"); err != nil {
		return nil, err
	}
	if err := run(ctx, logf, "systemctl", "enable", "--now", "mariadb"); err != nil {
		return nil, err
	}
	// Same hardening as mariadb-secure-installation; root keeps unix_socket auth.
	logf("hardening: removing anonymous users, remote root and the test database")
	if _, err := mariadb(ctx, `DELETE FROM mysql.global_priv WHERE User = '';
DELETE FROM mysql.global_priv WHERE User = 'root' AND Host NOT IN ('localhost', '127.0.0.1', '::1');
DROP DATABASE IF EXISTS test;
DELETE FROM mysql.db WHERE Db = 'test' OR Db = 'test\\_%';
FLUSH PRIVILEGES;`); err != nil {
		return nil, err
	}
	return dbStatus(ctx)
}

func dbHost(p map[string]string) (string, error) {
	h := p["host"]
	if h == "" {
		h = "localhost"
	}
	if !dbHostRe.MatchString(h) {
		return "", errors.New("host must be localhost, %, an IP or a subnet like 10.0.1.%")
	}
	return h, nil
}

func dbPassword(p map[string]string) (string, error) {
	pw := p["password"]
	if len(pw) < 12 || len(pw) > 128 {
		return "", errors.New("password must be 12-128 characters")
	}
	return pw, nil
}

func dbCreate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if !dbIdent.MatchString(name) || systemDBs[name] {
		return nil, errors.New("database name may only contain letters, digits and _ (max 64)")
	}
	sql := fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n", name)
	if user := p["user"]; user != "" {
		if !dbUserName.MatchString(user) || user == "root" {
			return nil, errors.New("user name may only contain letters, digits and _ (max 32)")
		}
		host, err := dbHost(p)
		if err != nil {
			return nil, err
		}
		pw, err := dbPassword(p)
		if err != nil {
			return nil, err
		}
		acct := sqlString(user) + "@" + sqlString(host)
		sql += fmt.Sprintf("CREATE USER IF NOT EXISTS %s IDENTIFIED BY %s;\nGRANT ALL PRIVILEGES ON `%s`.* TO %s;\nFLUSH PRIVILEGES;\n",
			acct, sqlString(pw), name, acct)
		logf("creating database %s with user %s@%s", name, user, host)
	} else {
		logf("creating database %s", name)
	}
	if _, err := mariadb(ctx, sql); err != nil {
		return nil, err
	}
	return dbStatus(ctx)
}

func dbDelete(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	name := p["name"]
	if !dbIdent.MatchString(name) || systemDBs[name] {
		return nil, errors.New("invalid database name")
	}
	logf("dropping database %s", name)
	sql := fmt.Sprintf("DROP DATABASE `%s`;\n", name)
	if p["drop_users"] == "true" {
		// Drop users whose only grant was this database.
		out, err := mariadb(ctx, fmt.Sprintf(`SELECT d.User, d.Host FROM mysql.db d WHERE d.Db = %s
  AND NOT EXISTS (SELECT 1 FROM mysql.db o WHERE o.User = d.User AND o.Host = d.Host AND o.Db <> d.Db);`, sqlString(name)))
		if err != nil {
			return nil, err
		}
		for _, r := range rows(out) {
			if len(r) == 2 && r[0] != "root" {
				logf("dropping user %s@%s", r[0], r[1])
				sql += fmt.Sprintf("DROP USER IF EXISTS %s@%s;\n", sqlString(r[0]), sqlString(r[1]))
			}
		}
	}
	if _, err := mariadb(ctx, sql); err != nil {
		return nil, err
	}
	return dbStatus(ctx)
}

func dbUserCreate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	user := p["user"]
	if !dbUserName.MatchString(user) || user == "root" {
		return nil, errors.New("invalid user name")
	}
	host, err := dbHost(p)
	if err != nil {
		return nil, err
	}
	pw, err := dbPassword(p)
	if err != nil {
		return nil, err
	}
	acct := sqlString(user) + "@" + sqlString(host)
	sql := fmt.Sprintf("CREATE USER IF NOT EXISTS %s IDENTIFIED BY %s;\nALTER USER %s IDENTIFIED BY %s;\n", acct, sqlString(pw), acct, sqlString(pw))
	for _, db := range strings.Fields(strings.ReplaceAll(p["databases"], ",", " ")) {
		if !dbIdent.MatchString(db) || systemDBs[db] {
			return nil, fmt.Errorf("invalid database %q", db)
		}
		sql += fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO %s;\n", db, acct)
	}
	sql += "FLUSH PRIVILEGES;\n"
	logf("saving user %s@%s", user, host)
	if _, err := mariadb(ctx, sql); err != nil {
		return nil, err
	}
	return dbStatus(ctx)
}

func dbUserDelete(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	user := p["user"]
	if !dbUserName.MatchString(user) || user == "root" {
		return nil, errors.New("invalid user name")
	}
	host, err := dbHost(p)
	if err != nil {
		return nil, err
	}
	logf("dropping user %s@%s", user, host)
	if _, err := mariadb(ctx, fmt.Sprintf("DROP USER %s@%s;\n", sqlString(user), sqlString(host))); err != nil {
		return nil, err
	}
	return dbStatus(ctx)
}
