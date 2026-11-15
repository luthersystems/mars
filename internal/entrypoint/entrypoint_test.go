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

func writeIDFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	group := filepath.Join(dir, "group")
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/bash\nmars:x:10001:10001::/opt/home:/usr/sbin/nologin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(group, []byte("root:x:0:\nmars:x:10001:"), 0o644); err != nil {
		t.Fatal(err)
	}
	return passwd, group
}

func TestUnknownUserEnvKnownUIDUnchanged(t *testing.T) {
	passwd, group := writeIDFiles(t)
	env := []string{"HOME=/opt/home"}
	got, err := UnknownUserEnv(env, 10001, 10001, passwd, group, "/lib/libnss_wrapper.so")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "HOME=/opt/home" {
		t.Fatalf("env = %v, want unchanged", got)
	}
}

func TestUnknownUserEnvWithoutLibUnchanged(t *testing.T) {
	passwd, group := writeIDFiles(t)
	got, err := UnknownUserEnv([]string{"HOME=/opt/home"}, 1234, 1234, passwd, group, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("env = %v, want unchanged", got)
	}
}

func TestUnknownUserEnvAddsEntries(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	passwd, group := writeIDFiles(t)
	env := []string{"HOME=/opt/home", "LD_PRELOAD=/lib/other.so", "PATH=/bin"}
	got, err := UnknownUserEnv(env, 1234, 5678, passwd, group, "/lib/libnss_wrapper.so")
	if err != nil {
		t.Fatal(err)
	}
	if v := lookupEnv(got, "LD_PRELOAD"); v != "/lib/other.so /lib/libnss_wrapper.so" {
		t.Fatalf("LD_PRELOAD = %q", v)
	}
	if lookupEnv(got, "PATH") != "/bin" || lookupEnv(got, "HOME") != "/opt/home" {
		t.Fatalf("env lost a value: %v", got)
	}
	p, err := os.ReadFile(lookupEnv(got, "NSS_WRAPPER_PASSWD"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(p), "mars:x:10001:10001::/opt/home:/usr/sbin/nologin\n"+PasswdEntry(1234, 5678, "/opt/home")+"\n") {
		t.Fatalf("passwd copy = %q", p)
	}
	g, err := os.ReadFile(lookupEnv(got, "NSS_WRAPPER_GROUP"))
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != "root:x:0:\nmars:x:10001:\n"+GroupEntry(5678)+"\n" {
		t.Fatalf("group copy = %q", g)
	}
	// A known gid is not added again.
	got, err = UnknownUserEnv(env, 1234, 10001, passwd, group, "/lib/libnss_wrapper.so")
	if err != nil {
		t.Fatal(err)
	}
	g, err = os.ReadFile(lookupEnv(got, "NSS_WRAPPER_GROUP"))
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != "root:x:0:\nmars:x:10001:" {
		t.Fatalf("group copy = %q, want the original", g)
	}
}

func TestHasIDMatchesOnlyTheIDField(t *testing.T) {
	data := []byte("user1234:x:1:1::/home/user1234:/bin/sh\n")
	if hasID(data, 1234) {
		t.Fatal("hasID matched a name, not the uid field")
	}
	if !hasID(data, 1) {
		t.Fatal("hasID missed uid 1")
	}
}
