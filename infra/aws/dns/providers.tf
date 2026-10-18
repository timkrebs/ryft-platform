# Route 53 is global; the region only matters for the provider's API endpoint.
provider "aws" {
  region = var.region
}
