# syntax=docker/dockerfile:1

FROM golang:1.22 AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/ermete ./cmd/ermete

FROM gcr.io/distroless/static-debian12
WORKDIR /
COPY --from=builder /out/ermete /ermete
USER nonroot:nonroot
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/ermete"]
