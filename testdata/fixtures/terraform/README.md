Terraform repositories for the apply tests, laid out as `<scm>/<org>/<repo>`.
Each is copied into a fresh git repository that acts as the remote. A root's
`show.json` stands in for `terraform show -json`, and its resources match
the Automation resources in `../scan.yaml`.
