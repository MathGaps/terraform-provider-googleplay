terraform {
  required_providers {
    googleplay = {
      source  = "mathgaps/googleplay"
      version = "~> 0.1"
    }
  }
}

# Credentials come from the GOOGLEPLAY_CREDENTIALS environment variable (the
# JSON text of a service account key) or from application default credentials.
provider "googleplay" {
  # Only googleplay_user and googleplay_app_grant need the developer account id:
  # the number after /developers/ in a Play Console URL.
  developer_id = "1234567890123456789"
}
