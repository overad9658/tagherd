terraform {
  backend "s3" {
    bucket = "acme-tfstate"
    key    = "dev.tfstate"
    region = "us-east-1"
  }
}
