# A literal tags map.
resource "aws_s3_bucket" "data" {
  bucket = "acme-data"

  tags = {
    Environment        = "prod"
    DeploymentType     = "Automation"
    AppCategory        = "Data"
    Exposure           = "Internal"
    AdminEmail         = "data@example.com"
    DataClassification = "Internal"
    SCM                = "GitHub"
    Org                = "acme"
    Repo               = "infra-aws"
  }
}

# Tags merged onto the common set.
resource "aws_dynamodb_table" "ledger" {
  name     = "ledger"
  hash_key = "id"

  attribute {
    name = "id"
    type = "S"
  }

  tags = merge(local.common_tags, {
    Environment        = "Production"
    AppCategory        = "Payments"
    Exposure           = "public"
    AdminEmail         = "dba@example.com"
    OwningOrg          = "Platform"
    DataClassification = "Internal"
  })
}

# Tags from a variable: tagherd cannot tell where to set them.
resource "aws_instance" "etl" {
  ami           = "ami-0001"
  instance_type = "t3.micro"
  tags          = var.etl_tags
}

variable "etl_tags" {
  type = map(string)
}

# The function's tags come in through the module call.
module "reports" {
  source = "../../modules/reports"
  name   = "report-gen"

  tags = merge(local.common_tags, {
    Environment = "Production"
    AppCategory = "Reporting"
    Exposure    = "Internal"
    AdminEmail  = "reports@example.com"
  })
}
