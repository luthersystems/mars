package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/luthersystems/mars/internal/alb"
	"github.com/luthersystems/mars/internal/ansible"
	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/packer"
	"github.com/luthersystems/mars/internal/runner"
	"github.com/luthersystems/mars/internal/terraform"
)

type CLI struct {
	Plan              terraform.PlanCmd              `cmd:"" help:"Run terraform plan."`
	Apply             terraform.ApplyCmd             `cmd:"" help:"Run terraform apply."`
	Destroy           terraform.DestroyCmd           `cmd:"" help:"Run terraform destroy."`
	Show              terraform.ShowCmd              `cmd:"" help:"Run terraform show."`
	Graph             terraform.GraphCmd             `cmd:"" help:"Run terraform graph."`
	Init              terraform.InitCmd              `cmd:"" help:"Run terraform init."`
	NewWorkspace      terraform.NewWorkspaceCmd      `cmd:"new-workspace" help:"Create a Terraform workspace for the target environment."`
	Taint             terraform.TaintCmd             `cmd:"" help:"Run terraform taint."`
	Untaint           terraform.UntaintCmd           `cmd:"" help:"Run terraform untaint."`
	Import            terraform.ImportCmd            `cmd:"" help:"Run terraform import."`
	Terraform         terraform.RawCmd               `cmd:"terraform" passthrough:"" help:"Run raw terraform command."`
	MigrationFromPlan terraform.MigrationFromPlanCmd `cmd:"" name:"migration_fromplan" help:"Generate tfedit migration file from a Terraform plan."`
	MigratePlan       terraform.MigratePlanCmd       `cmd:"" name:"migrate_plan" help:"Run tfmigrate plan."`
	MigrateApply      terraform.MigrateApplyCmd      `cmd:"" name:"migrate_apply" help:"Run tfmigrate apply."`

	AnsiblePlaybook     ansible.PlaybookCmd        `cmd:"ansible-playbook" help:"Run ansible-playbook with Mars defaults."`
	AnsibleExecute      ansible.ExecuteCmd         `cmd:"ansible-execute" help:"Run ansible with Mars defaults."`
	AnsibleVaultEncrypt ansible.VaultEncryptCmd    `cmd:"ansible-vault-encrypt" help:"Encrypt Ansible Vault text."`
	AnsibleVaultDecrypt ansible.VaultDecryptCmd    `cmd:"ansible-vault-decrypt" help:"Decrypt Ansible Vault text."`
	AnsibleVaultKey     ansible.VaultDecryptKeyCmd `cmd:"" name:"ansible-vault-decrypt-key" help:"Decrypt one key from a YAML file."`

	PackerValidate packer.ValidateCmd `cmd:"packer-validate" help:"Run packer validate in the target image directory."`
	PackerBuild    packer.BuildCmd    `cmd:"packer-build" help:"Run packer build in the target image directory."`

	ALBDNS alb.DNSCmd `cmd:"" name:"alb-dns" help:"Print matching ALB DNS names."`
}

func Main(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, r runner.Runner) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "no arguments provided")
		return 1
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "missing command")
		return 1
	}
	target, cleanArgs, verbosity, skipPrompt := extractTerraformGlobals(args)
	var root CLI
	parser, err := kong.New(
		&root,
		kong.Name("mars"),
		kong.Description("Luther Systems infrastructure management tool."),
		kong.Writers(stdout, stderr),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	parsed, err := parser.Parse(cleanArgs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return runner.ExitCode(err)
	}
	rt := &app.Runtime{
		Target:     target,
		Verbosity:  verbosity,
		SkipPrompt: skipPrompt,
		Runner:     r,
		Stdin:      stdin,
		Stdout:     stdout,
		Stderr:     stderr,
	}
	if err := parsed.Run(ctx, rt); err != nil {
		fmt.Fprintln(stderr, err)
		return runner.ExitCode(err)
	}
	return 0
}

func extractTerraformGlobals(args []string) (string, []string, int, bool) {
	target := args[0]
	clean := []string{}
	verbosity := 0
	skipPrompt := false
	i := 1
	for ; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--skip-prompt":
			skipPrompt = true
		case arg == "--verbose" || arg == "-v":
			verbosity++
		case strings.HasPrefix(arg, "-") && len(arg) > 2 && onlyV(arg[1:]):
			verbosity += len(arg) - 1
		default:
			clean = append(clean, args[i:]...)
			return target, clean, verbosity, skipPrompt
		}
	}
	return target, clean, verbosity, skipPrompt
}

func onlyV(s string) bool {
	for _, r := range s {
		if r != 'v' {
			return false
		}
	}
	return s != ""
}
