FROM golang:1.26.5 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /manager ./cmd

FROM scratch
USER 65532:65532
COPY --from=builder /manager /manager
ENTRYPOINT ["/manager"]
