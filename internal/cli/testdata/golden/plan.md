## tagherd plan

8 change(s) to 5 resource(s): 5 direct, 3 terraform. 4 unresolved. 1 scope(s) unreadable.

### Changes (8 of 8)

| Cloud | Scope | Type | Name | Tag | Action | Old | New | Rule | Route |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| AWS | 111122223333 | AWS Machine Image (AMI) | base-2026-09 | OwningOrg | ADD |  | Platform | builtin:ownership | terraform |
| AWS | 111122223333 | AWS RDS PostgreSQL Instance | orders | Environment | UPDATE | production | Production | builtin:case | direct |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | Brand | UPDATE |    | Acme | parent | direct |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | DeploymentType | ADD |  | Manual | builtin:deploymenttype | direct |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | Exposure | UPDATE | Public | External | builtin:synonym | direct |
| Azure | 00000000-0000-0000-0000-000000000001 | Azure Compute Virtual Machine | vm-app | EndpointType | UPDATE | server | Server | builtin:case | direct |
| GCP | acme-prod | GCP Compute Instance | batch-1 | tag:AdminEmail | ADD |  | ops@example.com | defaults:scope | terraform |
| GCP | acme-prod | GCP Compute Instance | batch-1 | environment | UPDATE | prod | Production | builtin:synonym | terraform |

### Unresolved (4 of 4)

| Cloud | Scope | Type | Name | Tag | Finding | Current | Reason |
| --- | --- | --- | --- | --- | --- | --- | --- |
| AWS | 111122223333 | AWS EC2 Instance | web-1 | Environment | DUPLICATE_KEY | Environment=Production, environment=Staging | not fixable automatically |
| AWS | 111122223333 | AWS Lambda Function | billing-api | Brand | CASE_MISMATCH | brand=Acme | DeploymentType is invalid, so nothing is written to this resource |
| AWS | 111122223333 | AWS Lambda Function | billing-api | DeploymentType | INVALID_DEPLOYMENT_TYPE | Scripted | not fixable automatically |
| AWS | 111122223333 | AWS S3 Bucket | acme-logs | AdminEmail | INVALID_VALUE | not-an-email | no synonym for this value |
