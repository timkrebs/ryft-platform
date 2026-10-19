# One Dockerfile for every binary; pick it with --build-arg SERVICE=ingest|scoring|api|notify|migrate
# The build stage runs on the build machine and cross-compiles for the target
# platform, so multi-arch builds (CI: linux/amd64 + linux/arm64) need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG SERVICE
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/${SERVICE} ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
COPY --from=build /out/${SERVICE} /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
