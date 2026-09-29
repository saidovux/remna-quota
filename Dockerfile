FROM golang:1.27 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/quota-api ./cmd/quota-api

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /out/quota-api /usr/local/bin/quota-api
USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/quota-api"]
