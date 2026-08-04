# Build environment
# -----------------
FROM golang:1.25.6-bookworm AS builder
LABEL stage=builder

ARG DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# chart-inspector local-replaces its sibling modules (../composition-dynamic-controller,
# ../core-provider), so the build context is the go/ workspace: copy the whole tree so the
# replaces resolve, then build the chart-inspector module.
WORKDIR /src
COPY . .
WORKDIR /src/chart-inspector
RUN go mod download
RUN CGO_ENABLED=0 GO111MODULE=on go build -a -o /bin/webservice ./main.go && strip /bin/webservice

# Deployment environment
# ----------------------
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /bin/webservice /bin/webservice
USER nonroot:nonroot
ENTRYPOINT ["/bin/webservice"]
