package alb

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/joho/godotenv"
	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

type DNSCmd struct {
	Verbose   int    `name:"verbose" short:"v" type:"counter" help:"Print matching diagnostics."`
	Region    string `name:"region" help:"AWS region containing ALBs."`
	Project   string `name:"project" help:"Only print ALBs with a matching project tag."`
	Component string `name:"component" help:"Only print ALBs with a matching component tag."`
	Org       string `name:"org" help:"Only print ALBs with a matching organization tag."`
	VarsFile  string `name:"vars-file" short:"f" help:"File with environment variable definitions."`
}

func (c *DNSCmd) Run(ctx context.Context, rt *app.Runtime) error {
	stdout := rt.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := rt.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if c.VarsFile != "" {
		if err := godotenv.Load(c.VarsFile); err != nil {
			return err
		}
	}
	region := c.Region
	if region == "" {
		region = os.Getenv("AWS_REGION")
	}
	project := c.Project
	if project == "" {
		project = os.Getenv("PROJECT_NAME")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return err
	}
	client := elbv2.NewFromConfig(cfg)
	match := map[string]string{
		"Project":      project,
		"Environment":  lutherEnv(rt.Target),
		"Component":    c.Component,
		"Organization": c.Org,
	}
	found, err := printMatching(ctx, client, stdout, stderr, match, c.Verbose > 0)
	if err != nil {
		return err
	}
	if found == 0 {
		return runner.Exit(1)
	}
	return nil
}

type elbClient interface {
	DescribeLoadBalancers(context.Context, *elbv2.DescribeLoadBalancersInput, ...func(*elbv2.Options)) (*elbv2.DescribeLoadBalancersOutput, error)
	DescribeTags(context.Context, *elbv2.DescribeTagsInput, ...func(*elbv2.Options)) (*elbv2.DescribeTagsOutput, error)
}

func printMatching(ctx context.Context, client elbClient, stdout io.Writer, stderr io.Writer, match map[string]string, verbose bool) (int, error) {
	found := 0
	var marker *string
	for {
		resp, err := client.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{
			Marker:   marker,
			PageSize: aws.Int32(20),
		})
		if err != nil {
			return 0, err
		}
		pageFound, err := printMatchingPage(ctx, client, stdout, stderr, resp.LoadBalancers, match, verbose)
		if err != nil {
			return 0, err
		}
		found += pageFound
		marker = resp.NextMarker
		if marker == nil {
			break
		}
	}
	return found, nil
}

func printMatchingPage(ctx context.Context, client elbClient, stdout io.Writer, stderr io.Writer, albs []elbv2types.LoadBalancer, match map[string]string, verbose bool) (int, error) {
	arns := make([]string, 0, len(albs))
	names := make(map[string]string, len(albs))
	for _, lb := range albs {
		arn := aws.ToString(lb.LoadBalancerArn)
		arns = append(arns, arn)
		names[arn] = aws.ToString(lb.DNSName)
	}
	if len(arns) == 0 {
		return 0, nil
	}
	resp, err := client.DescribeTags(ctx, &elbv2.DescribeTagsInput{ResourceArns: arns})
	if err != nil {
		return 0, err
	}
	found := 0
	for _, entry := range resp.TagDescriptions {
		tags := map[string]string{}
		for _, tag := range entry.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		if verbose {
			fmt.Fprintln(stderr, tags)
		}
		if matchesTags(tags, match) {
			found++
			fmt.Fprintln(stdout, names[aws.ToString(entry.ResourceArn)])
		}
	}
	return found, nil
}

func matchesTags(tags map[string]string, match map[string]string) bool {
	for key, value := range match {
		if value == "" {
			continue
		}
		if tags[key] != value {
			return false
		}
	}
	return true
}

func lutherEnv(env string) string {
	if env == "integration" {
		return "integ"
	}
	return env
}
