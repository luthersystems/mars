package secretstore

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

func AzureSecret(ctx context.Context, vaultName string, key string) (string, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", err
	}
	client, err := azsecrets.NewClient(fmt.Sprintf("https://%s.vault.azure.net", vaultName), cred, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.GetSecret(ctx, key, "", nil)
	if err != nil {
		return "", err
	}
	if resp.Value == nil {
		return "", fmt.Errorf("azure secret %q has no value", key)
	}
	return *resp.Value, nil
}

func AWSSecret(ctx context.Context, secretID string, region string, roleARN string) (string, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", err
	}
	if roleARN != "" {
		stsClient := sts.NewFromConfig(cfg)
		assumed, err := stsClient.AssumeRole(ctx, &sts.AssumeRoleInput{
			RoleArn:         aws.String(roleARN),
			RoleSessionName: aws.String("AssumeRoleVaultSession1"),
		})
		if err != nil {
			return "", err
		}
		cfg.Credentials = aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			aws.ToString(assumed.Credentials.AccessKeyId),
			aws.ToString(assumed.Credentials.SecretAccessKey),
			aws.ToString(assumed.Credentials.SessionToken),
		))
	}
	client := secretsmanager.NewFromConfig(cfg)
	resp, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(secretID),
		VersionStage: aws.String("AWSCURRENT"),
	})
	if err != nil {
		return "", err
	}
	if resp.SecretString == nil {
		return "", fmt.Errorf("aws secret %q has no SecretString", secretID)
	}
	return *resp.SecretString, nil
}
