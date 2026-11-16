FROM golang:1.24.4-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY *.go ./
COPY static/logo.jpg ./static/
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o meeseeks .

FROM alpine:latest

RUN apk --no-cache add ca-certificates
WORKDIR /root/

COPY --from=builder /app/meeseeks .

# Inside the container the port must be reachable; docker-run publishes it on 127.0.0.1 only.
ENV LISTEN_ADDR=0.0.0.0
EXPOSE 22282

CMD ["./meeseeks"]