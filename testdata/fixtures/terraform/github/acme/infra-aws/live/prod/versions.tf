terraform {
  backend "s3" {
    bucket = "acme-tfstate"
    key    = "prod.tfstate"
    region = "us-east-1"
  }
}

locals {
  common_tags = {
    Brand          = "Acme"
    DeploymentType = "Automation"
    SCM            = "GitHub"
    Org            = "acme"
    Repo           = "infra-aws"
  }
}
