#!/bin/bash

# Set variables
PROJECT_ID="cockroach-rob"
SA_NAME="chaos-mesh-sa"
SA_DISPLAY_NAME="Chaos Mesh Service Account"

# Create the service account
gcloud iam service-accounts create $SA_NAME \
  --display-name="$SA_DISPLAY_NAME" \
  --project=$PROJECT_ID

# Get the full service account email
SA_EMAIL=$(gcloud iam service-accounts list \
  --filter="displayName:$SA_DISPLAY_NAME" \
  --format='value(email)' \
  --project=$PROJECT_ID)

# Grant the necessary roles
gcloud projects add-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:$SA_EMAIL" \
  --role="roles/container.admin"

gcloud projects add-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:$SA_EMAIL" \
  --role="roles/storage.admin"

gcloud projects add-iam-policy-binding $PROJECT_ID \
  --member="serviceAccount:$SA_EMAIL" \
  --role="roles/compute.admin"

# Create and download a key for the service account
gcloud iam service-accounts keys create k8s-admin-sa-key.json \
  --iam-account=$SA_EMAIL \
  --project=$PROJECT_ID

echo "Service account created successfully. The key file 'k8s-admin-sa-key.json' has been saved in the current directory."
echo "Service account email: $SA_EMAIL"