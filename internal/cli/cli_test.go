package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/runner"
)

func TestTerraformPlanBuildsWorkspaceAndVarFiles(t *testing.T) {
	withProject(t, func(dir string) {
		writeFile(t, ".terraform-version", "1.7.3\n")
		writeFile(t, "vars/common/common.tfvars", "")
		writeFile(t, "vars/dev/dev.tfvars", "")
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("default\n")}}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{
			"dev", "--skip-prompt", "plan",
			"--destroy",
			"--out", "plan.out",
			"--target", "module.a",
			"--target", "module.b",
			"--refresh-only",
		}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		want := [][]string{
			{"tfenv", "install"},
			{"terraform", "workspace", "show"},
			{"terraform", "workspace", "select", "dev"},
			{"terraform", "plan", "-var-file=vars/common/common.tfvars", "-var-file=vars/dev/dev.tfvars", "-destroy", "-out=plan.out", "-target", "module.a", "-target", "module.b", "-refresh-only"},
		}
		if got := fake.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %#v, want %#v\nrecords:\n%s", got, want, fake.Output())
		}
	})
}

func TestTerraformRawCommandPassesFlagsAfterDoubleDash(t *testing.T) {
	withProject(t, func(dir string) {
		writeFile(t, ".terraform-version", "1.7.3\n")
		writeFile(t, "vars/dev/dev.tfvars", "")
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("dev\n")}}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{
			"dev", "terraform", "--", "providers", "--help",
		}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		wantLast := []string{"terraform", "providers", "--help"}
		if got := fake.Commands()[len(fake.Commands())-1]; !reflect.DeepEqual(got, wantLast) {
			t.Fatalf("last command = %#v, want %#v\nrecords:\n%s", got, wantLast, fake.Output())
		}
	})
}

func TestAnsiblePlaybookUsesInventoryAndVaultDefaults(t *testing.T) {
	withProject(t, func(dir string) {
		writeFile(t, "vault_password.txt", "password")
		writeFile(t, "inventories/dev/mars.yaml", strings.Join([]string{
			"ssh_user: ec2-user",
			"script: inventory.yml",
			"ssh_common_args:",
			"  - -oStrictHostKeyChecking=accept-new",
			"  - -oUserKnownHostsFile=/tmp/known_hosts",
			"",
		}, "\n"))
		fake := &runner.Fake{}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{
			"dev", "ansible-playbook", "site.yml",
			"--verbose",
			"--tags", "deploy",
			"--limit", "web",
			"--check",
			"--start-at-task", "Restart app",
		}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		want := [][]string{{
			"ansible-playbook", "-v", "site.yml",
			"--vault-password-file", "vault_password.txt",
			"-i", "inventories/dev/inventory.yml",
			"-u", "ec2-user",
			"--ssh-extra-args=-oStrictHostKeyChecking=accept-new -oUserKnownHostsFile=/tmp/known_hosts",
			"--check",
			"--tags", "deploy",
			"--limit", "web",
			"--start-at-task", "Restart app",
		}}
		if got := fake.Commands(); !reflect.DeepEqual(got, want) {
			t.Fatalf("commands = %#v, want %#v\nrecords:\n%s", got, want, fake.Output())
		}
	})
}

func TestAnsiblePlaybookUsesGoVaultIDHelpers(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantArg string
		wantEnv map[string]string
	}{
		{
			name: "aws",
			args: []string{
				"--aws-sm-secret-id", "secret-id",
				"--aws-region", "us-west-2",
				"--aws-role-arn", "role-arn",
			},
			wantArg: "/opt/mars/vault-aws-secretsmanager",
			wantEnv: map[string]string{
				"AWS_SM_SECRET_ID": "secret-id",
				"AWS_REGION":       "us-west-2",
				"AWS_ROLE_ARN":     "role-arn",
			},
		},
		{
			name: "azure",
			args: []string{
				"--az-vault", "vault-name",
				"--az-vault-key", "vault-key",
			},
			wantArg: "/opt/mars/vault-az-keyvault",
			wantEnv: map[string]string{
				"AZ_KEYVAULT_NAME": "vault-name",
				"AZ_KEYVAULT_KEY":  "vault-key",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withProject(t, func(dir string) {
				fake := &runner.Fake{}
				var stdout, stderr bytes.Buffer
				args := append([]string{"dev", "ansible-playbook", "site.yml"}, tt.args...)

				code := Main(context.Background(), args, strings.NewReader(""), &stdout, &stderr, fake)

				if code != 0 {
					t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
				}
				got := fake.Commands()[0]
				wantPrefix := []string{
					"ansible-playbook", "site.yml",
					"--vault-id", tt.wantArg,
					"-i", "inventories/dev/aws_ec2.yml",
				}
				if len(got) < len(wantPrefix) || !reflect.DeepEqual(got[:len(wantPrefix)], wantPrefix) {
					t.Fatalf("command = %#v, want prefix %#v", got, wantPrefix)
				}
				if !reflect.DeepEqual(fake.Records[0].Cmd.Env, tt.wantEnv) {
					t.Fatalf("env = %#v, want %#v", fake.Records[0].Cmd.Env, tt.wantEnv)
				}
			})
		})
	}
}

func TestAnsibleExecutePreservesArgsAppendBehavior(t *testing.T) {
	withProject(t, func(dir string) {
		writeFile(t, "vault_password.txt", "password")
		fake := &runner.Fake{}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{
			"dev", "ansible-execute", "all", "-m", "shell", "-a", "uptime", "-a", "whoami",
		}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		wantLast := []string{
			"ansible", "--vault-password-file", "vault_password.txt",
			"-i", "inventories/dev/aws_ec2.yml",
			"-u", "ubuntu",
			"--ssh-extra-args=-oStrictHostKeyChecking=no -oUserKnownHostsFile=/dev/null",
			"all", "-m", "shell", "uptime", "whoami",
		}
		if got := fake.Commands()[0]; !reflect.DeepEqual(got, wantLast) {
			t.Fatalf("command = %#v, want %#v", got, wantLast)
		}
	})
}

func TestPackerUsesTargetAsWorkingDirectory(t *testing.T) {
	withProject(t, func(dir string) {
		fake := &runner.Fake{}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{"image-dir", "packer-build", "--debug"}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		if got := fake.Records[0].Cmd.Dir; got != "image-dir" {
			t.Fatalf("cwd = %q, want image-dir", got)
		}
		want := []string{"packer", "build", "-debug", "packer.json"}
		if got := fake.Commands()[0]; !reflect.DeepEqual(got, want) {
			t.Fatalf("command = %#v, want %#v", got, want)
		}
	})
}

func TestAnsibleVaultDecryptKey(t *testing.T) {
	withProject(t, func(dir string) {
		writeFile(t, "vault_password.txt", "password")
		encrypted := strings.Join([]string{
			"$ANSIBLE_VAULT;1.1;AES256",
			"36346265376334373736313766326537633434343133653062313563663734306535643632663833",
			"3662386333663761343330333634623738623236383439340a643830323439633763346261623833",
			"37653037626638376535373363383130303165646438613661653962396465343033653261363164",
			"6133303566316331630a393466663462313933373935633331316566396231313633383533333532",
			"6165",
			"",
		}, "\n")
		writeFile(t, "secrets.yml", "foo: !vault |\n"+indent(encrypted, "  "))
		fake := &runner.Fake{}
		var stdout, stderr bytes.Buffer

		code := Main(context.Background(), []string{"dev", "ansible-vault-decrypt-key", "secrets.yml", "foo"}, strings.NewReader(""), &stdout, &stderr, fake)

		if code != 0 {
			t.Fatalf("exit code = %d, stderr:\n%s", code, stderr.String())
		}
		if got := stdout.String(); got != "secret\n" {
			t.Fatalf("stdout = %q, want secret newline", got)
		}
		if len(fake.Records) != 0 {
			t.Fatalf("vault decrypt should not run external commands, got %s", fake.Output())
		}
	})
}

func withProject(t *testing.T, fn func(dir string)) {
	t.Helper()
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(old); err != nil {
			t.Fatal(err)
		}
	})
	fn(dir)
}

func writeFile(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func indent(s string, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}
