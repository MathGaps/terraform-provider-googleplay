# Bringing an app that already exists in Play Console under management.
#
# The Google Play Developer API cannot create an app, so every configuration
# starts here: the app, its users and its testers already exist. Declare them
# as they are, import them, and check that `tofu plan` proposes no change
# before you change anything.

terraform {
  required_version = ">= 1.7"

  required_providers {
    googleplay = {
      source  = "mathgaps/googleplay"
      version = "~> 0.1"
    }
  }
}

variable "developer_id" {
  description = "The Play Console developer account id: the number after /developers/ in a Play Console URL."
  type        = string
}

variable "package_name" {
  description = "The package name of the existing app."
  type        = string
  default     = "com.example.app"
}

provider "googleplay" {
  developer_id = var.developer_id
}

# --- who can do what ----------------------------------------------------------

import {
  to = googleplay_user.release_manager
  id = "release-manager@example.com"
}

resource "googleplay_user" "release_manager" {
  email = "release-manager@example.com"
}

import {
  to = googleplay_app_grant.release_manager
  id = "release-manager@example.com/${var.package_name}"
}

resource "googleplay_app_grant" "release_manager" {
  email        = googleplay_user.release_manager.email
  package_name = var.package_name

  app_level_permissions = [
    "CAN_VIEW_NON_FINANCIAL_DATA",
    "CAN_MANAGE_TRACK_APKS",
    "CAN_MANAGE_TRACK_USERS",
  ]
}

# --- who can test -------------------------------------------------------------

import {
  to = googleplay_track_testers.internal
  id = "${var.package_name}/internal"
}

resource "googleplay_track_testers" "internal" {
  package_name  = var.package_name
  track         = "internal"
  google_groups = ["developers@example.com"]
}

# --- what is for sale ---------------------------------------------------------

import {
  to = googleplay_subscription.premium
  id = "${var.package_name}/premium"
}

resource "googleplay_subscription" "premium" {
  package_name = var.package_name
  product_id   = "premium"

  listings = {
    "en-US" = {
      title = "Premium"
    }
  }

  base_plans = {
    monthly = {
      state = "ACTIVE"

      auto_renewing = {
        billing_period_duration = "P1M"
      }

      regional_configs = {
        US = {
          price                       = { currency_code = "USD", amount = "4.99" }
          new_subscriber_availability = true
        }
      }
    }
  }
}
