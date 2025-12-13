FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o elly .

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/elly /usr/local/bin/
EXPOSE 9876
ENTRYPOINT ["elly", "-url", "0.0.0.0:9876", "-db", "/data/elly.db"]
