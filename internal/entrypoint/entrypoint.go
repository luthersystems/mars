package entrypoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const defaultUser = "default"

type Config struct {
	Home    string
	UserID  int
	GroupID int
	// UserIDSet is true when USER_ID is in the environment, so USER_ID=0
	// (ask for root) differs from no USER_ID.
	UserIDSet bool
}

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	uid, err := envInt(getenv, "USER_ID", 0)
	if err != nil {
		return Config{}, err
	}
	gid, err := envInt(getenv, "GROUP_ID", 0)
	if err != nil {
		return Config{}, err
	}
	return Config{
		UserID:    uid,
		GroupID:   gid,
		Home:      getenv("HOME"),
		UserIDSet: getenv("USER_ID") != "",
	}, nil
}

func Run(marsPath string, args []string, env []string) error {
	cfg, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if err := CheckStartUser(os.Geteuid(), cfg); err != nil {
		return err
	}
	if os.Geteuid() == 0 && cfg.UserID != 0 {
		if cfg.Home == "" {
			return errors.New("HOME must be set when USER_ID is non-zero")
		}
		if err := PrepareUser(cfg); err != nil {
			return err
		}
		if err := dropPrivileges(cfg); err != nil {
			return err
		}
	}
	if euid := os.Geteuid(); euid != 0 {
		env, err = UserEntryEnv(env, euid, os.Getegid(), "/etc/passwd", "/etc/group", NSSWrapperLib())
		if err != nil {
			return err
		}
	}
	return syscall.Exec(marsPath, append([]string{marsPath}, args...), env) //nolint:gosec // G204: execs the mars binary itself with the container's own args
}

// CheckStartUser fails when USER_ID asks for a user the container cannot
// become. The image starts as the non-root user mars (mars#322). A caller
// that sets USER_ID to switch users, or USER_ID=0 for root, must start the
// container as root (docker run --user 0:0, or runAsUser: 0 in Kubernetes):
// only root can write the passwd entry and switch. Without USER_ID, or with
// USER_ID equal to the user the container started as, mars runs as that
// user, which can be any uid (mars#363, see UserEntryEnv).
func CheckStartUser(euid int, cfg Config) error {
	if euid == 0 || !cfg.UserIDSet || cfg.UserID == euid {
		return nil
	}
	return fmt.Errorf("USER_ID=%d needs the container to start as root, but it started as uid %d: run it with --user 0:0 (docker) or runAsUser: 0 (Kubernetes), or leave USER_ID unset to run as uid %d", cfg.UserID, euid, euid)
}

func PrepareUser(cfg Config) error {
	if err := ensureLine("/etc/group", defaultUser+":", GroupEntry(cfg.GroupID)); err != nil {
		return err
	}
	if err := ensureLine("/etc/passwd", defaultUser+":", PasswdEntry(cfg.UserID, cfg.GroupID, cfg.Home)); err != nil {
		return err
	}
	return ChownTree(cfg.Home, cfg.UserID, cfg.GroupID)
}

// NSSWrapperLibGlob finds the nss_wrapper library of the image's
// libnss-wrapper package on either architecture.
const NSSWrapperLibGlob = "/usr/lib/*-linux-gnu/libnss_wrapper.so"

// NSSWrapperLib returns the path of the nss_wrapper library, or "" when the
// image has none.
func NSSWrapperLib() string {
	matches, _ := filepath.Glob(NSSWrapperLibGlob)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// UserEntryEnv gives the uid a passwd entry whose home is HOME, without root
// (mars#363). The image runs as any uid, for example docker run --user
// "$(id -u):$(id -g)". ssh (git over ssh, ansible) fails with "No user
// exists for uid" when the uid has no entry, and it reads ~/.ssh from the
// entry's home, not HOME, so an entry the image already has for the uid
// (ubuntu, uid 1000) must point at HOME too. Only root can write
// /etc/passwd, so this copies /etc/passwd and /etc/group to a new temporary
// directory, adds or fixes the uid's entry (as the user default when it is
// new) and the gid's group, and returns env with nss_wrapper preloaded to
// read the copies. env is returned unchanged when both entries are already
// right. When the copies cannot be made (no nss_wrapper in the image, no
// writable temporary directory), mars still runs: it prints a warning and
// returns env unchanged.
func UserEntryEnv(env []string, uid int, gid int, passwdPath string, groupPath string, lib string) ([]string, error) {
	passwd, err := os.ReadFile(passwdPath) //nolint:gosec // G304: passwdPath is /etc/passwd, set by Run
	if err != nil {
		return nil, err
	}
	group, err := os.ReadFile(groupPath) //nolint:gosec // G304: groupPath is /etc/group, set by Run
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	home := lookupEnv(env, "HOME")
	if home == "" {
		home = "/"
		env = setEnv(env, "HOME", home)
	}
	newPasswd, passwdChanged := withPasswdEntry(passwd, uid, gid, home)
	newGroup, groupChanged := group, false
	if !hasID(group, gid) {
		newGroup, groupChanged = appendLine(group, GroupEntry(gid)), true
	}
	if !passwdChanged && !groupChanged {
		return env, nil
	}
	warn := func(reason string) []string {
		fmt.Fprintf(os.Stderr, "mars-entrypoint: uid %d:%d has no passwd entry with home %s, and %s; ssh may fail\n", uid, gid, home, reason)
		return env
	}
	if lib == "" {
		return warn("the image has no nss_wrapper"), nil
	}
	dir, err := os.MkdirTemp("", "mars-nss-")
	if err != nil {
		return warn("it cannot be written: " + err.Error()), nil
	}
	passwdCopy := filepath.Join(dir, "passwd")
	groupCopy := filepath.Join(dir, "group")
	if err := os.WriteFile(passwdCopy, newPasswd, 0o644); err != nil { //nolint:gosec // G306: a passwd file is world-readable
		_ = os.RemoveAll(dir)
		return warn("it cannot be written: " + err.Error()), nil
	}
	if err := os.WriteFile(groupCopy, newGroup, 0o644); err != nil { //nolint:gosec // G306: a group file is world-readable
		_ = os.RemoveAll(dir)
		return warn("it cannot be written: " + err.Error()), nil
	}
	preload := lib
	if existing := lookupEnv(env, "LD_PRELOAD"); existing != "" {
		preload = existing + " " + lib
	}
	env = setEnv(env, "LD_PRELOAD", preload)
	env = setEnv(env, "NSS_WRAPPER_PASSWD", passwdCopy)
	env = setEnv(env, "NSS_WRAPPER_GROUP", groupCopy)
	return env, nil
}

// withPasswdEntry returns passwd with an entry for uid whose home is home,
// and whether it differs from passwd. An entry the uid already has keeps
// its name and gets home; a missing one is added as the user default.
func withPasswdEntry(passwd []byte, uid int, gid int, home string) ([]byte, bool) {
	want := strconv.Itoa(uid)
	lines := strings.Split(string(passwd), "\n")
	for i, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[2] != want {
			continue
		}
		if fields[5] == home {
			return passwd, false
		}
		fields[5] = home
		lines[i] = strings.Join(fields, ":")
		return []byte(strings.Join(lines, "\n")), true
	}
	return appendLine(passwd, PasswdEntry(uid, gid, home)), true
}

// hasID reports whether a passwd or group file has an entry whose third
// field (the uid or gid) is id.
func hasID(data []byte, id int) bool {
	want := strconv.Itoa(id)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 2 && fields[2] == want {
			return true
		}
	}
	return false
}

func appendLine(data []byte, line string) []byte {
	out := append([]byte{}, data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, line+"\n"...)
}

func lookupEnv(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value = v
		}
	}
	return value
}

func setEnv(env []string, key string, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+value)
}

func PasswdEntry(uid int, gid int, home string) string {
	return fmt.Sprintf("%s:x:%d:%d:Default User:%s:/usr/sbin/nologin", defaultUser, uid, gid, home)
}

func GroupEntry(gid int) string {
	return fmt.Sprintf("%s:x:%d:", defaultUser, gid)
}

func ChownTree(root string, uid int, gid int) error {
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid) //nolint:gosec // G122: runs as root at container start over the user's HOME; Lchown does not follow symlinks
	})
}

func envInt(getenv func(string) string, name string, fallback int) (int, error) {
	raw := getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be non-negative", name)
	}
	return value, nil
}

func ensureLine(path string, prefix string, line string) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is /etc/group or /etc/passwd, set by PrepareUser
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(existing, prefix) {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // G302,G304: /etc/group and /etc/passwd must be world-readable (0644)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(f, line); err != nil {
		_ = f.Close()
		return err
	}
	// A failed close can lose the appended line, so it is an error too.
	return f.Close()
}

func dropPrivileges(cfg Config) error {
	if err := syscall.Setgroups([]int{cfg.GroupID}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(cfg.GroupID); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}
	if err := syscall.Setuid(cfg.UserID); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}
	return nil
}
