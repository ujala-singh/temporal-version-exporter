# Build stage
FROM golang:1.25.1 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go env -w GOPROXY=https://proxy.golang.org,direct
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/temporal-version-exporter ./cmd/exporter

# Final stage
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/temporal-version-exporter /bin/temporal-version-exporter
EXPOSE 9090
USER nonroot:nonroot
ENTRYPOINT ["/bin/temporal-version-exporter"]
