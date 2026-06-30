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
	"sort"
	"strings"
	"time"

	"github.com/luthersystems/insideout-terraform-presets/cmd/insideout-import/reversedisco"
	"github.com/luthersystems/insideout-terraform-presets/pkg/composer/imported"
	"github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport"
	reversejob "github.com/luthersystems/insideout-terraform-presets/pkg/reverseimport/job"
)

const (
	DefaultOutputDir = "/marsproject/outputs/reverse-import"
	resultFile       = "reverse-result.json"
)

type Runner func(context.Context, reversejob.Request, reverseimport.Options) (reversejob.Result, error)

// discovererFactory builds the closure-capable cloud discoverer the
// reverse-import engine needs for selection-closure expansion and dep-chase.
// It mirrors reversedisco.New; the package var lets tests inject a fake so
// they can assert the engine Options are wired with a non-nil discoverer
// (and the customer assume-role auth) without standing up real cloud clients.
//
// luthersystems/mars#195: before this seam existed, Main never set
// Options.Discoverer or Options.ClosureDiscoverer, so the engine emitted the
// selection_closure_unavailable diagnostic and silently skipped closure +
// dep-chase.
//
// The trailing awsAuth carries the customer-account assume-role identity
// (presets#770). The Mars job pod runs as the platform Argo role; terraform
// reaches the customer account via generated assume_role { role_arn } blocks.
// The discoverer's direct AWS SDK calls must assume the SAME role, otherwise
// they run as the wrong principal and the customer account returns AccessDenied
// (presets#739 defect 2). Empty RoleARN means "use ambient credentials" — the
// correct behavior for the local CLI run directly with customer creds.
type discovererFactory func(ctx context.Context, cloud, region, gcpProjectID, awsEndpointURL string, awsAuth reversedisco.AWSAssumeRole) (reverseimport.Discoverer, func(), error)

// newDiscoverer is the production discoverer constructor. Tests override it to
// inject a fake; production code never reassigns it.
var newDiscoverer discovererFactory = reversedisco.New

// lazyDiscoverer defers constructing the real cloud discoverer until the engine
// first needs it (selection-closure expansion or dep-chase), which happens
// after the engine's cheap selection validation. It implements both
// reverseimport.Discoverer and reverseimport.ClosureDiscoverer so the engine
// treats it as closure-capable, yet building it costs nothing until a method is
// called — so a credential/API gap surfaces only on the closure path and does
// not mask the engine's structured validation result for an invalid selection
// (luthersystems/mars#195).
//
// Not safe for concurrent first-use; the engine drives closure then dep-chase
// sequentially within a single run, so this is sufficient.
type lazyDiscoverer struct {
	newDiscoverer  discovererFactory
	cloud          string
	region         string
	gcpProjectID   string
	awsEndpointURL string
	// awsAuth is the customer-account assume-role identity resolved eagerly at
	// Main (cheap env/file reads, no network) and threaded through to the
	// factory on first use so the discoverer's direct AWS SDK calls run as the
	// same principal terraform's generated provider blocks reach the customer
	// account with (presets#770 / #739). Empty RoleARN ⇒ ambient credentials.
	awsAuth reversedisco.AWSAssumeRole

	inner   reverseimport.Discoverer
	cleanup func()
	err     error
	built   bool
}

// resolve builds the underlying discoverer once, caching the result (and any
// construction error) for subsequent calls.
func (l *lazyDiscoverer) resolve(ctx context.Context) (reverseimport.Discoverer, error) {
	if !l.built {
		l.built = true
		l.inner, l.cleanup, l.err = l.newDiscoverer(ctx, l.cloud, l.region, l.gcpProjectID, l.awsEndpointURL, l.awsAuth)
		if l.err != nil {
			l.err = fmt.Errorf("build closure discoverer: %w", l.err)
		}
	}
	return l.inner, l.err
}

func (l *lazyDiscoverer) DiscoverByID(ctx context.Context, tfType, id, region, accountID string) (imported.ImportedResource, error) {
	d, err := l.resolve(ctx)
	if err != nil {
		return imported.ImportedResource{}, err
	}
	return d.DiscoverByID(ctx, tfType, id, region, accountID)
}

func (l *lazyDiscoverer) DiscoverClosure(ctx context.Context, req reverseimport.ClosureRequest) ([]imported.ImportedResource, error) {
	d, err := l.resolve(ctx)
	if err != nil {
		return nil, err
	}
	closer, ok := d.(reverseimport.ClosureDiscoverer)
	if !ok {
		// reversedisco.New always returns a closure-capable adapter; this guards
		// a future factory that does not.
		return nil, fmt.Errorf("discoverer %T does not support closure discovery", d)
	}
	return closer.DiscoverClosure(ctx, req)
}

// Close releases the underlying discoverer's resources if it was ever built.
func (l *lazyDiscoverer) Close() {
	if l.cleanup != nil {
		l.cleanup()
	}
}

// Compile-time proof that lazyDiscoverer satisfies both engine surfaces, so the
// engine never falls back to the selection_closure_unavailable diagnostic.
var (
	_ reverseimport.Discoverer        = (*lazyDiscoverer)(nil)
	_ reverseimport.ClosureDiscoverer = (*lazyDiscoverer)(nil)
)

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

	// Hand the engine a closure-capable cloud discoverer. Without it the engine
	// emits the selection_closure_unavailable diagnostic and skips both
	// dependency-closure expansion (the "auto-included N dependencies" behavior)
	// and dep-chase. The discoverer reuses the same cloud/region/endpoint config
	// the run already targets, so it talks to the same account/project the
	// import reads back. See luthersystems/mars#195.
	//
	// The cloud/region/GCP-project flags are optional overrides; when empty we
	// derive them from the request the same way the engine does
	// (reverseimport.Run derives cloud from resources[0].Identity), so the
	// discoverer is built for the right provider even when the dispatcher omits
	// the flags.
	//
	// Construction is lazy: the real cloud client (e.g. the GCP Cloud Asset gRPC
	// client, which needs ADC + an enabled API) is built on first use by the
	// engine's closure/dep-chase phases, which run AFTER the engine's cheap
	// selection validation (e.g. the InsideOutImported rejection). Eagerly
	// dialing the cloud here would let a credential/API gap mask the structured
	// validation result the engine produces for an invalid selection.
	cloud := firstNonEmpty(cfg.cloud, requestCloud(req))
	region := firstNonEmpty(cfg.region, requestRegion(req))
	gcpProjectID := firstNonEmpty(cfg.gcpProjectID, requestGCPProjectID(req))

	// Resolve the customer-account assume-role identity the discoverer's direct
	// AWS SDK calls must adopt. The Mars job pod runs as the platform Argo role;
	// terraform reaches the customer account via the generated provider's
	// assume_role { role_arn } blocks. The closure/dep-chase discoverer hits the
	// AWS SDK directly, so without the same assume-role hop it runs as the wrong
	// principal and the customer account returns AccessDenied — presets#739
	// defect 2, which the presets#770 graceful degradation now tolerates (a
	// diagnostic instead of killing the run), but lossily (no closure expansion).
	//
	// We resolve EAGERLY here because it is a cheap, network-free read of the
	// same sources the engine itself uses (ResolveAWSProviderAuth: the
	// TF_VAR_bootstrap_role / TF_VAR_aws_external_id env — present in the job pod
	// env — then outputs/cloud-provision.json, then tf/auto-vars). The cloud is
	// still DIALED lazily, so this does not undo the validate-before-dial
	// ordering above. An empty result (no RoleARN) is not an error: it means
	// "ambient credentials", the correct local-CLI behavior.
	//
	// Resolve ERRORS are unusual (corrupt JSON in the output-dir artifacts). We
	// log to stderr and proceed with empty (ambient) auth rather than aborting:
	// closure is best-effort, ambient creds in the pod just yield the same
	// AccessDenied the engine now degrades on, AND the engine's own
	// ResolveAWSProviderAuth call inside Run will surface the identical error
	// fatally if it actually matters for the provider blocks. Aborting here would
	// duplicate that fatal path and lose the engine's structured result.
	awsAuth := resolveAWSAssumeRole(cfg.outputDir, stderr)

	discoverer := &lazyDiscoverer{
		newDiscoverer:  newDiscoverer,
		cloud:          cloud,
		region:         region,
		gcpProjectID:   gcpProjectID,
		awsEndpointURL: cfg.awsEndpointURL,
		awsAuth:        awsAuth,
	}
	defer discoverer.Close()

	result, err := run(ctx, req, reverseimport.Options{
		OutputDir: cfg.outputDir,
		Workdir:   cfg.workDir,
		// Pass the request-derived cloud context (not just the raw flags) so the
		// engine and the discoverer built above agree on provider/region. The
		// engine derives these the same way when they are empty, so this is
		// purely making the two paths consistent.
		Cloud:        cloud,
		Region:       region,
		GCPProjectID: gcpProjectID,
		// DiscoverRegions scopes selection-closure discovery. For a
		// multi-region selection (no --region override) it carries every
		// distinct region across the selected resources so closure discovery
		// scans them all; the engine falls back to Region only when this is
		// empty. Mirrors the CLI reverse path.
		DiscoverRegions: requestRegions(req, cfg.region),
		AWSEndpointURL:  cfg.awsEndpointURL,
		ImportProjectID: cfg.importProjectID,
		ImportSessionID: cfg.importSessionID,
		ImportedAt:      time.Now().UTC(),
		TerraformBinary: cfg.terraformBinary,
		// The concrete discoverer implements both reverseimport.Discoverer
		// (dep-chase, DiscoverByID) and reverseimport.ClosureDiscoverer
		// (selection-closure expansion, DiscoverClosure); the engine resolves
		// the closure surface from Options.Discoverer when it is
		// closure-capable (pkg/reverseimport/closure.go), so setting Discoverer
		// wires both.
		Discoverer: discoverer,
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
	// A PARTIAL result is the partial-tolerant engine's designed-for outcome on a
	// whole-account import (presets#732/#734): reverseimport.Run imported every
	// plannable resource and ISOLATED the genuinely-unimportable ones — terraform
	// `generate-config-out` produced no resource body, an unsupported type, or a
	// first-import contract drop — into result.Resources with per-resource
	// diagnostics. That is a USABLE import: the stack state carries every imported
	// resource and the caller (ui-core → reliable → the import wizard) surfaces the
	// skipped/failed set from result.json. Failing the whole job on it defeats the
	// entire partial-tolerant engine.
	//
	// Before this, mars exited 1 on ANY non-succeeded status, so a whole-account
	// import FAILED whenever a single resource could not be generated. Triggering
	// case: account 141812438321 (staging session sess_v2_fvZSf5IfhLCb) imported
	// 361 resources cleanly but 2 orphans (an IAM role + a KMS key whose
	// generate-config-out produced no body) were skipped → status "partial" → mars
	// returned 1 → JOB_STATUS_FAILURE, even though the import was fine. See
	// reliable tf_start.go ReverseImportMarsVersion history (#835 publish-fix sweep).
	//
	// Only a hard failure (StatusFailed: zero imported / systemic) or an unknown
	// non-success status remains fatal. An empty status keeps the prior lenient
	// (success) behavior.
	switch result.Status {
	case "", reversejob.StatusSucceeded, reversejob.StatusPartial:
		// usable result — fall through to the success summary below
	default:
		err := fmt.Errorf("reverse import ended with status %q", result.Status)
		fmt.Fprintf(stderr, "insideout-reverse-import: %v\n", err)
		if writeErr := ensureFailureResult(cfg.outputDir, result, err); writeErr != nil {
			fmt.Fprintf(stderr, "insideout-reverse-import: write failure result: %v\n", writeErr)
		}
		return 1
	}

	if result.Status == reversejob.StatusPartial {
		fmt.Fprintf(stdout, "reverse import completed (partial): %d imported, %d add, %d change, %d destroy — some resources were skipped/failed and isolated; see result.json for per-resource diagnostics\n",
			result.PlanSummary.ImportCount,
			result.PlanSummary.AddCount,
			result.PlanSummary.ChangeCount,
			result.PlanSummary.DestroyCount)
		return 0
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

// resolveAWSAssumeRole resolves the customer-account assume-role identity from
// the same sources the reverse-import engine uses (env first, then the
// generated output-dir artifacts) and converts it to the
// reversedisco.AWSAssumeRole the discoverer factory takes. A resolve error is
// logged and downgraded to empty (ambient) auth — see the rationale at the call
// site in Main.
func resolveAWSAssumeRole(outputDir string, stderr io.Writer) reversedisco.AWSAssumeRole {
	auth, err := reverseimport.ResolveAWSProviderAuth(outputDir)
	if err != nil {
		fmt.Fprintf(stderr, "insideout-reverse-import: resolve AWS provider auth: %v; "+
			"proceeding with ambient credentials for closure discovery\n", err)
		return reversedisco.AWSAssumeRole{}
	}
	return reversedisco.AWSAssumeRole{
		RoleARN:    auth.RoleARN,
		ExternalID: auth.ExternalID,
	}
}

// firstNonEmpty returns the first non-blank string in order, or "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// requestCloud / requestRegion / requestGCPProjectID derive the provider
// context from the selected resources when the corresponding CLI flag is
// omitted, mirroring how reverseimport.Run derives cloud from the resource
// identities. The first resource carrying a non-empty value wins.
func requestCloud(req reversejob.Request) string {
	for _, r := range req.Resources {
		if c := strings.TrimSpace(r.Identity.Cloud); c != "" {
			return c
		}
	}
	return ""
}

func requestRegion(req reversejob.Request) string {
	for _, r := range req.Resources {
		if region := strings.TrimSpace(r.Identity.Region); region != "" {
			return region
		}
	}
	return ""
}

func requestGCPProjectID(req reversejob.Request) string {
	for _, r := range req.Resources {
		if p := strings.TrimSpace(r.Identity.ProjectID); p != "" {
			return p
		}
	}
	return ""
}

// requestRegions returns the set of regions closure discovery should scan,
// mirroring the CLI's reverse path (cmd/insideout-import/reverse.go). When the
// caller passes a single --region override it wins (the run targets one
// region); otherwise we collect every distinct region across the selected
// resources so closure discovery for a multi-region selection scans them all
// rather than just the first. The engine falls back to opts.Region only when
// this slice is empty, so a multi-region request without an override would
// otherwise silently discover children in just one region.
func requestRegions(req reversejob.Request, override string) []string {
	if o := strings.TrimSpace(override); o != "" {
		return []string{o}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, r := range req.Resources {
		region := strings.TrimSpace(r.Identity.Region)
		if region == "" {
			continue
		}
		if _, ok := seen[region]; ok {
			continue
		}
		seen[region] = struct{}{}
		out = append(out, region)
	}
	sort.Strings(out)
	return out
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
