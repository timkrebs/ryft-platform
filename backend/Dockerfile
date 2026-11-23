# One Dockerfile for every binary; pick it with --build-arg SERVICE=ingest|scoring|api|notify|migrate
# The build stage runs on the build machine and cross-compiles for the target
# platform, so multi-arch builds (CI: linux/amd64 + linux/arm64) need no emulation.
# Both base images are pinned by digest (the index, every platform), read off CI's logs on 2026-11-15
# (golang: Go 1.27 since D229, the `vulns` job's pull and the smoke build of PR #175); the tag stays
# for Dependabot, the digest decides. go.mod's `toolchain` line names this image's Go.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG SERVICE
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/${SERVICE} ./cmd/${SERVICE}
# Supabase's root CA (public; certs/supabase-ca.crt, pinned by internal/db/ca_test.go): the sslrootcert
# of DATABASE_URL on Cloud Run, which verifies the pooler (verify-full, D221). 0444 here, so the
# nonroot user reads it whatever mode the checkout gave the file; COPY --from keeps the mode.
RUN mkdir -p /out/etc/ryft && install -m 0444 certs/supabase-ca.crt /out/etc/ryft/supabase-ca.crt

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG SERVICE
COPY --from=build /out/${SERVICE} /app
COPY --from=build /out/etc/ryft/supabase-ca.crt /etc/ryft/supabase-ca.crt
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
