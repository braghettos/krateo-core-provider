#!/bin/bash

# KO_DOCKER_REPO=kind.local ko build --base-import-paths .  --preserve-import-paths
KO_DOCKER_REPO=kind.local ko build --base-import-paths .
# KO_DOCKER_REPO=kind.local KIND_CLUSTER_NAME=krateo-quickstart ko build --base-import-paths .
# KO_DOCKER_REPO=matteogastaldello ko build -t 0.1.4 --base-import-paths .
printf '\n\nList of current docker images loaded in KinD:\n'

kubectl get nodes krateo-quickstart-control-plane -o json \
    | jq -r '.status.images[] | " - " + .names[-1]'