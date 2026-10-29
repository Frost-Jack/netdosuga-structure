FROM golang:1.25.3-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /app .
FROM alpine:3.22.2
RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
COPY --from=build /app /app
USER 10001:10001
EXPOSE 50051 8081
ENTRYPOINT ["/app"]

