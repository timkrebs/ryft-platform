# One Dockerfile for every binary; pick it with --build-arg SERVICE=ingest|scoring|api|notify|migrate
# The build stage runs on the build machine and cross-compiles for the target
# platform, so multi-arch builds (CI: linux/amd64 + linux/arm64) need no emulation.
# Both base images are pinned by digest (the index, every platform), read off main's CI build log on
# 2026-11-15; the tag stays for Dependabot, the digest decides.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
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
