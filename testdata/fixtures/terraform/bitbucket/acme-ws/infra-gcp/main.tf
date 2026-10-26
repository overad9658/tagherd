terraform {
  backend "gcs" {
    bucket = "acme-tfstate"
    prefix = "infra-gcp"
  }
}

resource "google_compute_instance" "batch" {
  name         = "batch-1"
  machine_type = "e2-medium"
  zone         = "us-central1-a"

  boot_disk {
    initialize_params {
      image = "debian-cloud/debian-12"
    }
  }

  network_interface {
    network = "default"
  }

  labels = {
    environment    = "prod"
    deploymenttype = "automation"
    brand          = "acme"
    appcategory    = "batch"
    exposure       = "internal"
    owningorg      = "platform"
    endpointtype   = "server"
    scm            = "bitbucket"
    org            = "acme-ws"
    repo           = "infra-gcp"
  }
}
