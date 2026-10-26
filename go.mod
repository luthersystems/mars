module github.com/luthersystems/mars

go 1.26.0

toolchain go1.26.10

require (
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets v1.5.0
	github.com/alecthomas/kong v1.16.1
	github.com/aws/aws-sdk-go-v2 v1.47.4
	github.com/aws/aws-sdk-go-v2/config v1.33.10
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.63.5
	github.com/aws/aws-sdk-go-v2/service/iam v1.64.5
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.50.6
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.5
	github.com/aws/smithy-go v1.28.6
	github.com/hashicorp/terraform-json v0.29.0
	github.com/joho/godotenv v1.5.1
	github.com/luthersystems/insideout-terraform-presets v0.12.0
	github.com/sosedoff/ansible-vault-go v0.2.0
	golang.org/x/oauth2 v0.37.0
	google.golang.org/api v0.308.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/accesscontextmanager v1.15.0 // indirect
	cloud.google.com/go/asset v1.28.0 // indirect
	cloud.google.com/go/auth v0.24.1-0.20261001053825-dbc26066f70a // indirect
	cloud.google.com/go/auth/oauth2adapt v0.3.0 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	cloud.google.com/go/iam v1.12.0 // indirect
	cloud.google.com/go/longrunning v1.2.0 // indirect
	cloud.google.com/go/orgpolicy v1.20.0 // indirect
	cloud.google.com/go/osconfig v1.22.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.3 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal v1.2.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.10.1 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/apparentlymart/go-textseg/v17 v17.0.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.16 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/acm v1.43.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/apigateway v1.42.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.37.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.70.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/bedrock v1.66.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/cloudcontrol v1.32.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.67.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.81.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider v1.67.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.62.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.318.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/eks v1.90.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.48.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.27 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.35 // indirect
	github.com/aws/aws-sdk-go-v2/service/kms v1.55.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/lambda v1.101.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/opensearch v1.75.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/opensearchserverless v1.34.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/resourceexplorer2 v1.27.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi v1.35.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/route53 v1.65.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.106.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/servicediscovery v1.43.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/wafv2 v1.77.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-git/go-billy/v5 v5.9.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.23 // indirect
	github.com/googleapis/gax-go/v2 v2.26.2 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/hcl v0.0.0-20170504190234-a4b07c25de5f // indirect
	github.com/hashicorp/hcl/v2 v2.24.0 // indirect
	github.com/hashicorp/terraform-config-inspect v0.0.0-20260224005459-813a97530220 // indirect
	github.com/hashicorp/terraform-exec v0.25.2 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/zclconf/go-cty v1.19.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.67.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	google.golang.org/genproto v0.0.0-20260715232425-e75dac1f907d // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260817212433-ac3dfec99bb1 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/grpc v1.85.0-dev.0.20260825072537-93e31b48545e // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
