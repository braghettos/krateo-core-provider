#!/bin/bash

KO_DOCKER_REPO=kind.local KIND_CLUSTER_NAME=kind ko build --base-import-paths .
# KO_DOCKER_REPO=docker-username ko build -t 1.2.4 --base-import-paths .
printf '\n\nList of current docker images loaded in KinD:\n'

kubectl get nodes kind-control-plane -o json \
    | jq -r '.status.images[] | " - " + .names[-1]'