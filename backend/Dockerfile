# One Dockerfile for all three services; pick the binary with --build-arg SERVICE=ingest|scoring|api
FROM golang:1.25-alpine AS build
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/${SERVICE} ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
ARG SERVICE
COPY --from=build /out/${SERVICE} /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]
