## tagherd apply

5 written, 0 already set, 0 stale, 0 failed. Terraform: 6 proposed, 5 unroutable. Verify: 1 finding(s) still open.

### Changes (16 of 16)

| Cloud | Scope | Type | Name | Tag | New | Outcome | Detail |
| --- | --- | --- | --- | --- | --- | --- | --- |
| AWS | 111122223333 | AWS DynamoDB Table | audit | OwningOrg | Platform | unroutable | not_allowlisted: GitHub/acme/legacy-infra is not in terraform.allowed_repositories |
| AWS | 111122223333 | AWS DynamoDB Table | ledger | Exposure | External | proposed | patches/github-acme-infra-aws.patch |
| AWS | 111122223333 | AWS EC2 Instance | etl-1 | OwningOrg | Platform | unroutable | unsupported_expression: tags = var.etl_tags, which tagherd does not edit |
| AWS | 111122223333 | AWS Lambda Function | report-gen | OwningOrg | Platform | proposed | patches/github-acme-infra-aws.patch |
| AWS | 111122223333 | AWS Machine Image (AMI) | base-2026-09 | OwningOrg | Platform | unroutable | missing_repo_tags: the resource needs SCM, Org and Repo tags |
| AWS | 111122223333 | AWS RDS PostgreSQL Instance | orders | Environment | Production | written |  |
| AWS | 111122223333 | AWS S3 Bucket | acme-data | Brand | Acme | proposed | patches/github-acme-infra-aws.patch |
| AWS | 111122223333 | AWS S3 Bucket | acme-data | Environment | Production | proposed | patches/github-acme-infra-aws.patch |
| AWS | 111122223333 | AWS S3 Bucket | acme-data | OwningOrg | Platform | proposed | patches/github-acme-infra-aws.patch |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | Brand | Acme | written |  |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | DeploymentType | Manual | written |  |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | Exposure | External | written |  |
| AWS | 111122223333 | AWS S3 Bucket | acme-tmp | OwningOrg | Platform | unroutable | not_in_state: not in the state of 2 root(s): live/dev, live/prod |
| Azure | 00000000-0000-0000-0000-000000000001 | Azure Compute Virtual Machine | vm-app | EndpointType | Server | written |  |
| GCP | acme-prod | GCP Compute Instance | batch-1 | tag:AdminEmail | ops@example.com | unroutable | unsupported_expression: Resource Manager tags are bound by google_tags_tag_binding resources, which tagherd does not edit |
| GCP | acme-prod | GCP Compute Instance | batch-1 | environment | Production | proposed | patches/bitbucket-acme-ws-infra-gcp.patch |

### Still open after the writes (1)

| Cloud | Scope | Type | Name | Tag | Finding | Current |
| --- | --- | --- | --- | --- | --- | --- |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | AdminEmail | INVALID_VALUE | not-an-email |
