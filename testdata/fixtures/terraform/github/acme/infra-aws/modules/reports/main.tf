variable "name" {
  type = string
}

variable "tags" {
  type = map(string)
}

resource "aws_lambda_function" "this" {
  function_name = var.name
  role          = "arn:aws:iam::111122223333:role/reports"
  handler       = "index.handler"
  runtime       = "nodejs20.x"
  filename      = "lambda.zip"
  tags          = var.tags
}
