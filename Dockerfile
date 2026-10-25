FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go rdap-bootstrap.json ./
COPY templates ./templates
COPY static ./static
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /domain-search .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
COPY --from=build /domain-search /usr/local/bin/domain-search
USER app
EXPOSE 8080
CMD ["domain-search", "-addr", ":8080"]
