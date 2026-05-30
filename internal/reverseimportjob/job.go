package reverseimportjob

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport"
	reversejob "github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport/job"
)

const (
	DefaultOutputDir = "/marsproject/outputs/reverse-import"
	resultFile       = "reverse-result.json"
)

type Runner func(context.Context, reversejob.Request, reverseimport.Options) (reversejob.Result, error)

func Main(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, run Runner) int {
	if run == nil {
		run = reverseimport.Run
	}

	cfg, code, ok := parseArgs(args, stderr)
	if !ok {
		return code
	}

	req, err := readRequest(cfg.requestPath)
	if err != nil {
		fmt.Fprintf(stderr, "insideout-reverse-import: %v\n", err)
		return 1
	}

	result, err := run(ctx, req, reverseimport.Options{
		OutputDir:       cfg.outputDir,
		Workdir:         cfg.workDir,
		Cloud:           cfg.cloud,
		Region:          cfg.region,
		GCPProjectID:    cfg.gcpProjectID,
		AWSEndpointURL:  cfg.awsEndpointURL,
		ImportProjectID: cfg.importProjectID,
		ImportSessionID: cfg.importSessionID,
		ImportedAt:      time.Now().UTC(),
		TerraformBinary: cfg.terraformBinary,
		// Stream the engine's live phase progress + terraform subprocess
		// output to the job's stdout so Oracle's follow=1 stream (and the
		// InsideOut import wizard's log console) shows continuous progress
		// for the whole run, not just the final plan. See
		// luthersystems/mars#178.
		Stdout: stdout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "insideout-reverse-import: %v\n", err)
		if writeErr := ensureFailureResult(cfg.outputDir, result, err); writeErr != nil {
			fmt.Fprintf(stderr, "insideout-reverse-import: write failure result: %v\n", writeErr)
		}
		return 1
	}
	if result.Status != "" && result.Status != reversejob.StatusSucceeded {
		err := fmt.Errorf("reverse import ended with status %q", result.Status)
		fmt.Fprintf(stderr, "insideout-reverse-import: %v\n", err)
		if writeErr := ensureFailureResult(cfg.outputDir, result, err); writeErr != nil {
			fmt.Fprintf(stderr, "insideout-reverse-import: write failure result: %v\n", writeErr)
		}
		return 1
	}

	fmt.Fprintf(stdout, "reverse import succeeded: %d imported, %d add, %d change, %d destroy\n",
		result.PlanSummary.ImportCount,
		result.PlanSummary.AddCount,
		result.PlanSummary.ChangeCount,
		result.PlanSummary.DestroyCount)
	return 0
}

type config struct {
	requestPath     string
	outputDir       string
	workDir         string
	cloud           string
	region          string
	gcpProjectID    string
	awsEndpointURL  string
	importProjectID string
	importSessionID string
	terraformBinary string
}

func parseArgs(args []string, stderr io.Writer) (config, int, bool) {
	var cfg config
	fs := flag.NewFlagSet("insideout-reverse-import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, `Usage:
  insideout-reverse-import --request request.json [flags]

Flags:`)
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.requestPath, "request", "", "path to a JSON reverseimport/job.Request")
	fs.StringVar(&cfg.outputDir, "out-dir", DefaultOutputDir, "directory for reverse-import artifacts")
	fs.StringVar(&cfg.workDir, "work-dir", "", "optional Terraform generated-config work directory")
	fs.StringVar(&cfg.cloud, "cloud", "", "cloud provider override: aws or gcp")
	fs.StringVar(&cfg.region, "region", "", "AWS region or GCP provider region override")
	fs.StringVar(&cfg.gcpProjectID, "gcp-project-id", "", "GCP project ID override")
	fs.StringVar(&cfg.awsEndpointURL, "aws-endpoint-url", "", "AWS endpoint URL override, for example LocalStack")
	fs.StringVar(&cfg.importProjectID, "import-project-id", "", "InsideOut import project ID for provenance")
	fs.StringVar(&cfg.importSessionID, "import-session-id", "", "InsideOut import session ID for provenance")
	fs.StringVar(&cfg.terraformBinary, "terraform-binary", "", "path to terraform binary")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0, false
		}
		return cfg, 1, false
	}
	if strings.TrimSpace(cfg.requestPath) == "" {
		fmt.Fprintln(stderr, "insideout-reverse-import: --request is required")
		return cfg, 1, false
	}
	if strings.TrimSpace(cfg.outputDir) == "" {
		fmt.Fprintln(stderr, "insideout-reverse-import: --out-dir must not be empty")
		return cfg, 1, false
	}
	return cfg, 0, true
}

func readRequest(path string) (reversejob.Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return reversejob.Request{}, fmt.Errorf("open --request: %w", err)
	}
	defer f.Close()
	req, err := reversejob.DecodeRequest(f)
	if err != nil {
		return reversejob.Request{}, fmt.Errorf("decode --request: %w", err)
	}
	return req, nil
}

func ensureFailureResult(outputDir string, result reversejob.Result, runErr error) error {
	if strings.TrimSpace(outputDir) == "" {
		return nil
	}
	path := filepath.Join(outputDir, resultFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if result.Version == 0 {
		result.Version = reversejob.Version
	}
	result.Status = reversejob.StatusFailed
	result.Diagnostics = append(result.Diagnostics, reversejob.Diagnostic{
		Severity: "error",
		Code:     "reverse_import_failed",
		Message:  runErr.Error(),
	})
	result.Artifacts.ResultJSON = &reversejob.Artifact{
		Name:      resultFile,
		Path:      path,
		MediaType: "application/json",
	}
	body, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}
