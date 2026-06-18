FROM golang:1.22-bookworm AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=linux go build -o /baremetal-api ./cmd/api

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl ipmitool && rm -rf /var/lib/apt/lists/*
COPY --from=build /baremetal-api /baremetal-api
EXPOSE 8080
ENTRYPOINT ["/baremetal-api"]
