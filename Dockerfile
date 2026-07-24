# Multi-stage: a full Go toolchain builds the binary, then a near-empty
# runtime carries only the binary. The image Kubernetes schedules is the
# second stage — the compiler never ships.

FROM golang:1.26 AS build
WORKDIR /src

# Dependencies change rarely, code often: copy just the manifests and download
# first, so the module layer stays cached across ordinary code edits.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 makes a static binary — no libc, nothing to link at runtime —
# which is what lets the runtime stage be distroless/static below.
# -trimpath keeps build-host paths out of the binary (smaller, reproducible).
RUN CGO_ENABLED=0 go build -trimpath -o /gateway ./cmd/gateway

# distroless/static:nonroot is ~2 MB of CA certs and tzdata — no shell, no
# package manager, no libc. No shell means most container-escape and
# supply-chain tricks have nothing to grab; nonroot because a process that
# never needs root should never have it.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /gateway /gateway

EXPOSE 8080
ENTRYPOINT ["/gateway"]
