package ansible

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
	"github.com/luthersystems/mars/internal/secretstore"
	ansiblevault "github.com/sosedoff/ansible-vault-go"
	"gopkg.in/yaml.v3"
)

type VaultOpts struct {
	AZVault      string `name:"az-vault"`
	AZVaultKey   string `name:"az-vault-key"`
	AWSSecretID  string `name:"aws-sm-secret-id"`
	AWSRegion    string `name:"aws-region"`
	AWSRoleARN   string `name:"aws-role-arn"`
	resolvedOnce bool
}

type PlaybookCmd struct {
	VaultOpts   `embed:""`
	Path        string `arg:"" help:"Playbook path."`
	Debug       bool   `name:"debug"`
	Verbose     int    `name:"verbose" short:"v" type:"counter"`
	Tags        string `name:"tags"`
	Limit       string `name:"limit"`
	Check       bool   `name:"check"`
	StartAtTask string `name:"start-at-task"`
}

type ExecuteCmd struct {
	VaultOpts   `embed:""`
	HostPattern string   `arg:"" name:"host_pattern"`
	Module      string   `name:"module" short:"m" default:"ping"`
	Args        []string `name:"args" short:"a"`
	Verbose     int      `name:"verbose" short:"v" type:"counter"`
}

type VaultEncryptCmd struct {
	VaultOpts `embed:""`
	Path      string `name:"path"`
}

type VaultDecryptCmd struct {
	VaultOpts `embed:""`
	Path      string `name:"path"`
}

type VaultDecryptKeyCmd struct {
	VaultOpts `embed:""`
	YAMLFile  string `arg:"" name:"yaml_file"`
	Key       string `arg:"" name:"key"`
}

type service struct {
	env    string
	runner runner.Runner
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

type inventoryConfig struct {
	SSHUser       *string
	SSHCommonArgs []string
	Script        *string
}

func (c *PlaybookCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := c.VaultOpts.resolveAndValidate(); err != nil {
		return err
	}
	cmd := []string{"ansible-playbook"}
	if c.Debug {
		cmd = append(cmd, "-vvvv")
	} else if c.Verbose > 0 {
		cmd = append(cmd, "-"+strings.Repeat("v", c.Verbose))
	}
	cmd = append(cmd, c.Path)
	extra, err := s.commandContextArgs(c.VaultOpts)
	if err != nil {
		return err
	}
	cmd = append(cmd, extra...)
	if c.Check {
		cmd = append(cmd, "--check")
	}
	if c.Tags != "" {
		cmd = append(cmd, "--tags", c.Tags)
	}
	if c.Limit != "" {
		cmd = append(cmd, "--limit", c.Limit)
	}
	if c.StartAtTask != "" {
		cmd = append(cmd, "--start-at-task", c.StartAtTask)
	}
	return s.run(ctx, runner.Command{Argv: cmd, Env: c.VaultOpts.envVars()})
}

func (c *ExecuteCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := c.VaultOpts.resolveAndValidate(); err != nil {
		return err
	}
	cmd := []string{"ansible"}
	if c.Verbose > 0 {
		cmd = append(cmd, "-"+strings.Repeat("v", c.Verbose))
	}
	extra, err := s.commandContextArgs(c.VaultOpts)
	if err != nil {
		return err
	}
	cmd = append(cmd, extra...)
	cmd = append(cmd, c.HostPattern, "-m", c.Module)
	cmd = append(cmd, c.Args...)
	return s.run(ctx, runner.Command{Argv: cmd, Env: c.VaultOpts.envVars()})
}

func (c *VaultEncryptCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := c.VaultOpts.resolveAndValidate(); err != nil {
		return err
	}
	key, err := s.encryptionKey(ctx, c.VaultOpts)
	if err != nil {
		return err
	}
	secret, err := s.secretInput(c.Path)
	if err != nil {
		return err
	}
	encrypted, err := ansiblevault.Encrypt(secret, key)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.stdout, encrypted)
	return nil
}

func (c *VaultDecryptCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := c.VaultOpts.resolveAndValidate(); err != nil {
		return err
	}
	key, err := s.encryptionKey(ctx, c.VaultOpts)
	if err != nil {
		return err
	}
	encrypted, err := s.encryptedInput(c.Path)
	if err != nil {
		return err
	}
	return s.decryptAndPrint(key, encrypted)
}

func (c *VaultDecryptKeyCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := c.VaultOpts.resolveAndValidate(); err != nil {
		return err
	}
	key, err := s.encryptionKey(ctx, c.VaultOpts)
	if err != nil {
		return err
	}
	encrypted, err := vaultValue(c.YAMLFile, c.Key)
	if err != nil {
		return err
	}
	return s.decryptAndPrint(key, encrypted)
}

func (v *VaultOpts) resolveAndValidate() error {
	if v.resolvedOnce {
		return nil
	}
	v.resolvedOnce = true
	if v.AWSSecretID == "" {
		v.AWSSecretID = os.Getenv("AWS_SM_SECRET_ID")
	}
	if v.AWSRegion == "" {
		v.AWSRegion = os.Getenv("AWS_REGION")
	}
	if v.AWSRoleARN == "" {
		v.AWSRoleARN = os.Getenv("AWS_ROLE_ARN")
	}
	if (v.AZVault == "") != (v.AZVaultKey == "") {
		return errors.New("--az-vault and --az-vault-key must be supplied together")
	}
	if (v.AWSSecretID == "") != (v.AWSRegion == "") {
		return errors.New("--aws-sm-secret-id and --aws-region must be supplied together")
	}
	return nil
}

func (v VaultOpts) envVars() map[string]string {
	if v.AZVault != "" {
		return map[string]string{
			"AZ_KEYVAULT_NAME": v.AZVault,
			"AZ_KEYVAULT_KEY":  v.AZVaultKey,
		}
	}
	if v.AWSSecretID != "" {
		return map[string]string{
			"AWS_SM_SECRET_ID": v.AWSSecretID,
			"AWS_REGION":       v.AWSRegion,
			"AWS_ROLE_ARN":     v.AWSRoleARN,
		}
	}
	return nil
}

func newService(rt *app.Runtime) *service {
	stdin := rt.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := rt.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := rt.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return &service{
		env:    rt.Target,
		runner: rt.Runner,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}
}

func (s *service) run(ctx context.Context, cmd runner.Command) error {
	fmt.Fprintln(s.stderr, runner.ShellCommand(cmd.Argv))
	cmd.Quiet = true
	cmd.Stdout = s.stdout
	cmd.Stderr = s.stderr
	cmd.Stdin = s.stdin
	return s.runner.Run(ctx, cmd)
}

func (s *service) encryptionKey(ctx context.Context, opts VaultOpts) (string, error) {
	if opts.AZVault != "" {
		return secretstore.AzureSecret(ctx, opts.AZVault, opts.AZVaultKey)
	}
	if opts.AWSSecretID != "" {
		return secretstore.AWSSecret(ctx, opts.AWSSecretID, opts.AWSRegion, opts.AWSRoleARN)
	}
	path, err := s.findVaultPasswordFile()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *service) commandContextArgs(opts VaultOpts) ([]string, error) {
	var args []string
	vaultArgs, err := s.ansibleVaultArgs(opts)
	if err != nil {
		return nil, err
	}
	args = append(args, vaultArgs...)
	inventoryArgs, err := s.inventoryArgs()
	if err != nil {
		return nil, err
	}
	args = append(args, inventoryArgs...)
	args = append(args, s.sshUserArgs()...)
	args = append(args, s.sshCommonArgs()...)
	return args, nil
}

func (s *service) ansibleVaultArgs(opts VaultOpts) ([]string, error) {
	if opts.AZVault != "" {
		return []string{"--vault-id", "/opt/mars/vault-az-keyvault.py"}, nil
	}
	if opts.AWSSecretID != "" {
		return []string{"--vault-id", "/opt/mars/vault-aws-secretsmanager.py"}, nil
	}
	path, err := s.findVaultPasswordFile()
	if err != nil {
		return nil, err
	}
	return []string{"--vault-password-file", path}, nil
}

func (s *service) findVaultPasswordFile() (string, error) {
	files, err := filepath.Glob("*_vault_password.txt")
	if err != nil {
		return "", err
	}
	if _, err := os.Stat("vault_password.txt"); err == nil {
		files = append(files, "vault_password.txt")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("no remote vault key supplied and vault password file could not be located")
	}
	if len(files) > 1 {
		return "", fmt.Errorf("unable to determine vault password file from ambiguous entries %v", files)
	}
	return files[0], nil
}

func (s *service) inventoryArgs() ([]string, error) {
	cfg, err := s.readInventoryConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Script == nil {
		return nil, errors.New("missing inventory script for env")
	}
	return []string{"-i", filepath.Join("inventories", s.env, *cfg.Script)}, nil
}

func (s *service) sshUserArgs() []string {
	cfg, err := s.readInventoryConfig()
	if err != nil || cfg.SSHUser == nil {
		return nil
	}
	return []string{"-u", *cfg.SSHUser}
}

func (s *service) sshCommonArgs() []string {
	cfg, err := s.readInventoryConfig()
	if err != nil || cfg.SSHCommonArgs == nil {
		return nil
	}
	return []string{"--ssh-extra-args=" + strings.Join(cfg.SSHCommonArgs, " ")}
}

func (s *service) readInventoryConfig() (inventoryConfig, error) {
	sshUser := "ubuntu"
	script := "aws_ec2.yml"
	cfg := inventoryConfig{
		SSHUser:       &sshUser,
		SSHCommonArgs: []string{"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null"},
		Script:        &script,
	}
	path := filepath.Join("inventories", s.env, "mars.yaml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return cfg, err
	}
	if len(root.Content) == 0 {
		return cfg, nil
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return cfg, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		val := node.Content[i+1]
		switch key {
		case "ssh_user":
			if val.Kind == yaml.ScalarNode && val.Tag != "!!null" {
				v := val.Value
				cfg.SSHUser = &v
			} else {
				cfg.SSHUser = nil
			}
		case "script":
			if val.Kind == yaml.ScalarNode && val.Tag != "!!null" {
				v := val.Value
				cfg.Script = &v
			} else {
				cfg.Script = nil
			}
		case "ssh_common_args":
			if val.Tag == "!!null" {
				cfg.SSHCommonArgs = nil
				continue
			}
			var args []string
			if err := val.Decode(&args); err != nil {
				return cfg, err
			}
			cfg.SSHCommonArgs = args
		}
	}
	return cfg, nil
}

func (s *service) secretInput(path string) (string, error) {
	data, err := s.readSecretInput(path, "expected to read a secret from stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func (s *service) encryptedInput(path string) (string, error) {
	data, err := s.readSecretInput(path, "expected to read an encrypted secret from stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}

func (s *service) readSecretInput(path string, stdinErr string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	if !stdinPipe(s.stdin) {
		return nil, errors.New(stdinErr)
	}
	return io.ReadAll(s.stdin)
}

func (s *service) decryptAndPrint(key string, encrypted string) error {
	plain, err := ansiblevault.Decrypt(encrypted, key)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.stdout, plain)
	return nil
}

func stdinPipe(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return true
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeNamedPipe != 0
}

func vaultValue(path string, key string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("vault file %s does not contain a mapping", path)
	}
	mapping := root.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1].Value, nil
		}
	}
	return "", fmt.Errorf("key %q not found in %s", key, path)
}
