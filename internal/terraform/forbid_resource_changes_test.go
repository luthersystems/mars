package terraform

import (
	"context"
	"strings"
	"testing"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// TestAssertNoResourceChanges pins the core classification: state-only plan
// actions (no-op, read, forget) are allowed; any create/update/delete (incl.
// replace) is refused.
func TestAssertNoResourceChanges(t *testing.T) {
	rc := func(addr string, actions ...string) string {
		quoted := make([]string, len(actions))
		for i, a := range actions {
			quoted[i] = `"` + a + `"`
		}
		return `{"address":"` + addr + `","change":{"actions":[` + strings.Join(quoted, ",") + `]}}`
	}
	plan := func(changes ...string) []byte {
		return []byte(`{"format_version":"1.2","resource_changes":[` + strings.Join(changes, ",") + `]}`)
	}

	cases := []struct {
		name      string
		json      []byte
		wantErr   bool
		errSubstr string
	}{
		{"empty plan", plan(), false, ""},
		{"single forget", plan(rc("aws_s3_bucket.imp", "forget")), false, ""},
		{"multiple forgets", plan(rc("aws_s3_bucket.a", "forget"), rc("aws_s3_bucket.b", "forget")), false, ""},
		{"no-op", plan(rc("aws_s3_bucket.imp", "no-op")), false, ""},
		{"read", plan(rc("data.aws_caller_identity.cur", "read")), false, ""},
		{"create -> refuse", plan(rc("aws_iam_role.x", "create")), true, "aws_iam_role.x"},
		{"update -> refuse", plan(rc("aws_s3_bucket.imp", "update")), true, "aws_s3_bucket.imp"},
		{"delete -> refuse", plan(rc("aws_s3_bucket.imp", "delete")), true, "aws_s3_bucket.imp"},
		{"replace -> refuse", plan(rc("aws_db_instance.db", "create", "delete")), true, "aws_db_instance.db"},
		{"forget + create -> refuse names the create only", plan(rc("aws_s3_bucket.imp", "forget"), rc("aws_iam_role.x", "create")), true, "aws_iam_role.x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertNoResourceChanges(&strings.Builder{}, tc.json)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q does not mention %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	if err := assertNoResourceChanges(&strings.Builder{}, []byte("not json")); err == nil {
		t.Fatalf("expected parse error on malformed plan json")
	}
	// A forget alongside a create must NOT be silently allowed: the create
	// makes the whole apply unsafe.
	if err := assertNoResourceChanges(&strings.Builder{}, []byte(`{"format_version":"1.2","resource_changes":[{"address":"x","change":{"actions":["forget"]}},{"address":"y","change":{"actions":["delete"]}}]}`)); err == nil || !strings.Contains(err.Error(), "y") {
		t.Fatalf("forget+delete must be refused naming the delete; got %v", err)
	}
}

// TestForbidResourceChanges_FlowBlocksOnChange verifies the guarded apply
// inspects the plan and REFUSES (no `terraform apply` run) when the plan
// would change a resource.
func TestForbidResourceChanges_FlowBlocksOnChange(t *testing.T) {
	withProject(t, func() {
		writeFile(t, ".terraform-version", "1.7.5\n")
		writeFile(t, "vars/dev/dev.tfvars", "")
		planJSON := []byte(`{"format_version":"1.2","resource_changes":[{"address":"aws_iam_role.x","change":{"actions":["create"]}}]}`)
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("dev\n"), planJSON}}
		rt := &app.Runtime{
			Target: "dev", Runner: fake,
			Stdin: strings.NewReader(""), Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
			Clock: fixedClock(123), SkipPrompt: true,
		}
		err := (&ApplyCmd{Approve: true, ForbidResourceChanges: true}).Run(context.Background(), rt)
		if err == nil {
			t.Fatalf("expected refusal, got nil\nrecords:\n%s", fake.Output())
		}
		if !strings.Contains(err.Error(), "aws_iam_role.x") {
			t.Fatalf("error should name the offending resource: %v", err)
		}
		for _, cmd := range fake.Commands() {
			if len(cmd) >= 2 && cmd[0] == "terraform" && cmd[1] == "apply" {
				t.Fatalf("must NOT run `terraform apply` when changes are present; ran %v", cmd)
			}
		}
	})
}

// TestForbidResourceChanges_FlowAppliesWhenStateOnly verifies the guarded
// apply proceeds to `terraform apply <plan>` when the plan is state-only
// (a forget).
func TestForbidResourceChanges_FlowAppliesWhenStateOnly(t *testing.T) {
	withProject(t, func() {
		writeFile(t, ".terraform-version", "1.7.5\n")
		writeFile(t, "vars/dev/dev.tfvars", "")
		planJSON := []byte(`{"format_version":"1.2","resource_changes":[{"address":"aws_s3_bucket.imp","change":{"actions":["forget"]}}]}`)
		fake := &runner.Fake{CaptureOut: [][]byte{[]byte("dev\n"), planJSON}}
		rt := &app.Runtime{
			Target: "dev", Runner: fake,
			Stdin: strings.NewReader(""), Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
			Clock: fixedClock(123), SkipPrompt: true,
		}
		err := (&ApplyCmd{Approve: true, ForbidResourceChanges: true}).Run(context.Background(), rt)
		if err != nil {
			t.Fatalf("state-only apply should succeed: %v\nrecords:\n%s", err, fake.Output())
		}
		applied := false
		for _, cmd := range fake.Commands() {
			if len(cmd) == 3 && cmd[0] == "terraform" && cmd[1] == "apply" && strings.Contains(cmd[2], ".tf-plans") {
				applied = true
			}
		}
		if !applied {
			t.Fatalf("expected `terraform apply <savedplan>` for a state-only plan\nrecords:\n%s", fake.Output())
		}
	})
}
