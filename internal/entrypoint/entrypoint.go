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
	UserID  int
	GroupID int
	Home    string
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
		UserID:  uid,
		GroupID: gid,
		Home:    getenv("HOME"),
	}, nil
}

func Run(marsPath string, args []string, env []string) error {
	cfg, err := ConfigFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	if cfg.UserID != 0 {
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
	return syscall.Exec(marsPath, append([]string{marsPath}, args...), env)
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
		return os.Lchown(path, uid, gid)
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
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, existing := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(existing, prefix) {
			return nil
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
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
