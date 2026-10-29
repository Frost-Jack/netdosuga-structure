FROM golang:1.25.3-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /service .
FROM alpine:3.22.2
RUN addgroup -g 10001 app && adduser -D -H -u 10001 -G app app
COPY --from=build /service /service
USER 10001:10001
ENTRYPOINT ["/service"]

