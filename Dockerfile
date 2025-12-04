FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/server .
COPY config.env .

RUN addgroup -S 1000 && adduser -S 1000 -G 1000
RUN mkdir -p /home/1000
RUN chown -R 1000:1000 /home/1000

CMD ["./server"]