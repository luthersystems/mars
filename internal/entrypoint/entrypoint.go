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
	return syscall.Exec(marsPath, append([]string{marsPath}, args...), env) //nolint:gosec // G204: execs the mars binary itself with the container's own args
}

// CheckStartUser fails when USER_ID asks for a user the container cannot
// become. The image starts as the non-root user mars (mars#322). A caller
// that sets USER_ID to switch users, or USER_ID=0 for root, must start the
// container as root (docker run --user 0:0, or runAsUser: 0 in Kubernetes):
// only root can write the passwd entry and switch. Without USER_ID, or with
// USER_ID equal to the user the container started as, mars runs as that
// user.
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
