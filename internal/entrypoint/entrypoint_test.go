package entrypoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFromEnvDefaultsAndParsesIDs(t *testing.T) {
	env := map[string]string{
		"USER_ID":  "1234",
		"GROUP_ID": "5678",
		"HOME":     "/opt/home",
	}
	cfg, err := ConfigFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UserID != 1234 || cfg.GroupID != 5678 || cfg.Home != "/opt/home" {
		t.Fatalf("config = %#v", cfg)
	}

	cfg, err = ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UserID != 0 || cfg.GroupID != 0 {
		t.Fatalf("default config = %#v, want root IDs", cfg)
	}
}

func TestConfigFromEnvRejectsInvalidIDs(t *testing.T) {
	_, err := ConfigFromEnv(func(key string) string {
		if key == "USER_ID" {
			return "abc"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "USER_ID") {
		t.Fatalf("error = %v, want USER_ID parse error", err)
	}
}

func TestPasswdAndGroupEntries(t *testing.T) {
	if got := PasswdEntry(1234, 5678, "/opt/home"); got != "default:x:1234:5678:Default User:/opt/home:/usr/sbin/nologin" {
		t.Fatalf("passwd entry = %q", got)
	}
	if got := GroupEntry(5678); got != "default:x:5678:" {
		t.Fatalf("group entry = %q", got)
	}
}

func TestEnsureLineAppendsOnlyWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(path, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureLine(path, "default:", "default:x:1234:1234:Default User:/opt/home:/usr/sbin/nologin"); err != nil {
		t.Fatal(err)
	}
	if err := ensureLine(path, "default:", "default:x:9999:9999:Default User:/opt/home:/usr/sbin/nologin"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "default:"); got != 1 {
		t.Fatalf("default entries = %d, want 1\n%s", got, data)
	}
	if !strings.Contains(string(data), "default:x:1234:1234") {
		t.Fatalf("passwd content = %s", data)
	}
}

func TestCheckStartUser(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		euid    int
		wantErr bool
	}{
		{"root, no USER_ID", Config{}, 0, false},
		{"root, USER_ID=0", Config{UserIDSet: true}, 0, false},
		{"root, USER_ID=501", Config{UserID: 501, UserIDSet: true}, 0, false},
		{"non-root, no USER_ID", Config{}, 10001, false},
		{"non-root, USER_ID is itself", Config{UserID: 10001, UserIDSet: true}, 10001, false},
		{"non-root, USER_ID=0", Config{UserIDSet: true}, 10001, true},
		{"non-root, other USER_ID", Config{UserID: 501, UserIDSet: true}, 10001, true},
	}
	for _, tc := range cases {
		err := CheckStartUser(tc.euid, tc.cfg)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.name, err, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "--user 0:0") {
			t.Errorf("%s: error %q does not say how to start as root", tc.name, err)
		}
	}
}

func TestConfigFromEnvUserIDSet(t *testing.T) {
	cfg, err := ConfigFromEnv(func(key string) string {
		if key == "USER_ID" {
			return "0"
		}
		return ""
	})
	if err != nil || !cfg.UserIDSet {
		t.Fatalf("USER_ID=0: cfg = %#v, err = %v, want UserIDSet", cfg, err)
	}
	cfg, err = ConfigFromEnv(func(string) string { return "" })
	if err != nil || cfg.UserIDSet {
		t.Fatalf("no USER_ID: cfg = %#v, err = %v, want UserIDSet false", cfg, err)
	}
}
