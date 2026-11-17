package infrarepo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/luthersystems/mars/internal/runner"
)

// The repo_clone_ssh_url auto-var names the -infra repo. cloud-provision
// writes it to tf/auto-vars/git_repo.auto.tfvars.json.
const repoURLVar = "repo_clone_ssh_url"

// autoVarsDir is the template's tf/auto-vars.
func (r *Repo) autoVarsDir() string {
	return filepath.Join(r.Root, "tf", "auto-vars")
}

// tfVar is the template's getTfVar: the value of key in the first file that
// has a non-empty, non-null one, from tf/auto-vars/*.json and then the
// gitignored secrets/*.auto.tfvars.json credential drop (#160). A file that
// is not a JSON object is skipped. A string prints raw (jq -r), any other
// value as compact JSON. Without tf/auto-vars it reports the error and
// returns "", as the shell's command substitution did.
func (r *Repo) tfVar(key string) string {
	dir := r.autoVarsDir()
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		r.warn("ERROR: Terraform auto vars directory '%s' not found", dir)
		return ""
	}
	auto, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	secrets, _ := filepath.Glob(filepath.Join(r.Root, "secrets", "*.auto.tfvars.json"))
	for _, f := range append(auto, secrets...) {
		if v := jsonValue(f, key); v != "" && v != "null" {
			return v
		}
	}
	return ""
}

func jsonValue(path, key string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the project's own auto-vars files
	if err != nil {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return ""
	}
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return buf.String()
}

// tokenFile is the GitHub App token file of this step, if it has one
// (luthersystems/ui-infrastructure#442): GITHUB_GIT_TOKEN_FILE for the
// custom-stack apply, GITHUB_TOKEN_FILE for the stages with the github
// provider. The token itself never goes into .git/config, which travels in
// the project archive; a credential helper reads the file when git asks.
func (r *Repo) tokenFile() (string, bool) {
	for _, key := range []string{"GITHUB_GIT_TOKEN_FILE", "GITHUB_TOKEN_FILE"} {
		f := r.getenv(key)
		if f == "" {
			continue
		}
		fh, err := os.Open(f) //nolint:gosec // G304: the token file the environment names
		if err != nil {
			continue
		}
		_ = fh.Close()
		return f, true
	}
	return "", false
}

var sshGitHubURL = regexp.MustCompile(`^git@github\.com:(.+)$`)

// httpsURL is the HTTPS form of a git@github.com: URL; any other URL stays.
func httpsURL(url string) string {
	if m := sshGitHubURL.FindStringSubmatch(url); m != nil {
		return "https://github.com/" + m[1]
	}
	return url
}

// credentialHelper gives git the token in file for each request, as the
// x-access-token user.
func credentialHelper(file string) string {
	return `!f() { test "$1" = get || return 0; echo username=x-access-token; echo "password=$(tr -d '\n' < ` +
		runner.ShellQuote(file) + `)"; }; f`
}

const credentialKey = "credential.https://github.com.helper" //nolint:gosec // G101: a git config key, not a credential

// authArgs are the "-c" options that give one git command the token of
// this step (none without a token file). The empty helper first drops any
// helper from another config file.
func (r *Repo) authArgs() []string {
	tok, ok := r.tokenFile()
	if !ok {
		return nil
	}
	return []string{"-c", credentialKey + "=", "-c", credentialKey + "=" + credentialHelper(tok)}
}

// remoteURL is the -infra repo's URL for this step: HTTPS when the step has
// a token, else the recorded SSH URL; "" when none is recorded.
func (r *Repo) remoteURL() string {
	url := r.tfVar(repoURLVar)
	if url == "" {
		return ""
	}
	if _, ok := r.tokenFile(); ok {
		return httpsURL(url)
	}
	return url
}

func (r *Repo) hasGitDir() bool {
	_, err := os.Stat(filepath.Join(r.Root, ".git"))
	return err == nil
}

func (r *Repo) deployKey() string {
	return filepath.Join(r.Root, "secrets", "infra_deploy_key.pem")
}

// configure is the template's configure_git: the commit identity,
// core.fileMode off, git access to the -infra repo and its "infra" remote.
// It returns false only when the project has no .git (nothing can be
// committed or pushed); its individual git config writes are best effort,
// as in the shell, where only that check could fail it.
//
// core.fileMode is off because an archive or a mount can extract every file
// as 0777 (or drop the bit); stageTree decides exec bits itself.
func (r *Repo) configure(ctx context.Context) bool {
	if !r.hasGitDir() {
		// The greppable signal for #143 hypothesis (2): cloneInfraRepo never
		// ran, or its clone failed.
		r.warn("⚠️  [git-infra] WARNING: configure_git: no .git at %s — commit/push to the -infra repo will be SKIPPED. [sandbox-infrastructure-template#143]", r.Root)
		return false
	}
	if r.gitQuiet(ctx, "config", "--local", "user.email") != nil {
		_ = r.gitQuiet(ctx, "config", "--local", "user.email", "devbot@luthersystems.com")
	}
	if r.gitQuiet(ctx, "config", "--local", "user.name") != nil {
		_ = r.gitQuiet(ctx, "config", "--local", "user.name", "Luther DevBot")
	}
	_ = r.gitQuiet(ctx, "config", "--local", "core.fileMode", "false")
	r.configureAuth(ctx)
	r.configureRemote(ctx)
	return true
}

func (r *Repo) configureAuth(ctx context.Context) {
	if tok, ok := r.tokenFile(); ok {
		_ = r.gitQuiet(ctx, "config", "--local", "--unset-all", credentialKey)
		_ = r.gitQuiet(ctx, "config", "--local", "--add", credentialKey, "")
		_ = r.gitQuiet(ctx, "config", "--local", "--add", credentialKey, credentialHelper(tok))
		// A deploy key from an earlier run must not take over.
		_ = r.gitQuiet(ctx, "config", "--local", "--unset", "core.sshCommand")
		r.info("🔑 [git-infra] using the GitHub App token over HTTPS")
		return
	}
	key := r.deployKey()
	if st, err := os.Stat(key); err != nil || !st.Mode().IsRegular() {
		r.info("Skipping SSH config: no deploy key at %s", key)
		return
	}
	_ = r.gitQuiet(ctx, "config", "--local", "core.sshCommand", "ssh -i "+key+" -o IdentitiesOnly=yes")
}

func (r *Repo) configureRemote(ctx context.Context) {
	url := r.remoteURL()
	if url == "" {
		// The greppable signal for #143 hypothesis (1): the auto-var did not
		// reach this working tree, so the -infra repo is never wired.
		r.warn("⚠️  [git-infra] WARNING: repo_clone_ssh_url is empty in %s — 'infra' remote NOT configured; the per-project -infra repo will NOT be populated. [sandbox-infrastructure-template#143]", r.autoVarsDir())
		return
	}
	if r.hasRemote(ctx, "infra") {
		_ = r.gitQuiet(ctx, "remote", "set-url", "infra", url)
	} else {
		_ = r.gitQuiet(ctx, "remote", "add", "infra", url)
	}
	r.info("🔧 [git-infra] infra remote configured → %s", url)
}
