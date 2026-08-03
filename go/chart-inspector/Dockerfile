# Build environment
# -----------------
FROM golang:1.25.6-bookworm AS builder
LABEL stage=builder

ARG DEBIAN_FRONTEND=noninteractive

SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# hadolint ignore=DL3008
WORKDIR /src

COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# COPY apis/ apis/
COPY internal/ internal/
COPY docs/ docs/
COPY main.go main.go

# Build
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -o /bin/webservice ./main.go && \
    strip /bin/webservice

# Deployment environment
# ----------------------
FROM gcr.io/distroless/static:nonroot


COPY --from=builder /bin/webservice /bin/webservice

USER nonroot:nonroot

ENTRYPOINT ["/bin/webservice"]